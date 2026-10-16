package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

// addModuleCommands adds a command for every action the server declares:
// "audio.volume.set" becomes "hostctl audio volume set". Required
// arguments can be given in order as positional arguments; every argument
// also has a --flag.
func (a *app) addModuleCommands(ctx context.Context, root *cobra.Command) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	manifests, err := c.Manifests(ctx)
	if err != nil {
		return err
	}
	for _, m := range manifests {
		for _, spec := range m.Actions {
			segs := strings.Split(spec.Type, ".")
			if builtins[segs[0]] {
				continue // still reachable with hostctl action
			}
			parent := root
			for _, seg := range segs[:len(segs)-1] {
				parent = child(parent, seg, "")
			}
			leaf := child(parent, segs[len(segs)-1], spec.Description)
			a.makeActionCommand(leaf, spec, m.Name)
		}
	}
	a.addReadCommands(root, manifests)
	describeGroups(root)
	return nil
}

// describeGroups gives a group made from action names ("hostctl audio
// output") the names of its commands as its description.
func describeGroups(cmd *cobra.Command) {
	var names []string
	for _, c := range cmd.Commands() {
		describeGroups(c)
		if !c.Hidden && c.Name() != "help" && c.Name() != "completion" {
			names = append(names, c.Name())
		}
	}
	if cmd.Short == "" && len(names) > 0 {
		cmd.Short = strings.Join(names, ", ")
	}
}

func child(parent *cobra.Command, name, short string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	c := &cobra.Command{Use: name, Short: short, Args: cobra.ArbitraryArgs, RunE: groupRun}
	parent.AddCommand(c)
	return c
}

// groupRun runs for a command that only groups others: with no arguments
// it shows help; anything else is a typo, which must not exit 0.
func groupRun(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	return usageError{fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())}
}

type prop struct {
	name, typ, desc string
	// media is a string argument's content type (JSON Schema
	// contentMediaType): a document, which can be given as @file or -.
	media string
}

func schemaInfo(raw json.RawMessage) (props []prop, required []string) {
	var s struct {
		Properties map[string]struct {
			Type        any    `json:"type"`
			Description string `json:"description"`
			Media       string `json:"contentMediaType"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(raw, &s)
	for name, p := range s.Properties {
		typ, _ := p.Type.(string) // a type list ("string","null") is treated as untyped
		props = append(props, prop{name, typ, p.Description, p.Media})
	}
	sort.Slice(props, func(i, j int) bool { return props[i].name < props[j].name })
	return props, s.Required
}

func (a *app) makeActionCommand(cmd *cobra.Command, spec sdk.ActionSpec, module string) {
	props, required := schemaInfo(spec.Schema)
	// Secret arguments are read from standard input, never from the
	// command line (shell history, the process list).
	secret := map[string]bool{}
	for _, name := range spec.Secret {
		secret[name] = true
	}
	var secrets []string
	props = slices.DeleteFunc(props, func(p prop) bool {
		if secret[p.name] {
			secrets = append(secrets, p.name)
		}
		return secret[p.name]
	})
	required = slices.DeleteFunc(slices.Clone(required), func(r string) bool { return secret[r] })
	types := map[string]string{}
	media := map[string]bool{}
	values := map[string]*string{}
	bools := map[string]*bool{}
	for _, p := range props {
		types[p.name] = p.typ
		media[p.name] = p.media != ""
		flagName := strings.ReplaceAll(p.name, "_", "-")
		desc := p.desc
		if desc == "" {
			desc = p.typ
		}
		if p.media != "" {
			desc += " (@file reads a file, - standard input)"
		}
		if p.typ == "boolean" {
			bools[p.name] = cmd.Flags().Bool(flagName, false, desc)
		} else {
			values[p.name] = cmd.Flags().String(flagName, "", desc)
		}
	}
	var expect int64
	cmd.Flags().Int64Var(&expect, "expect-version", -1, "refuse if the resource is not at this version")

	// Positional arguments: the required ones, in order; or, for an action
	// with none required and one non-boolean argument, that one, optional
	// ("hostctl window close [instance]").
	positional := required
	use := cmd.Name()
	for _, r := range required {
		use += " <" + r + ">"
	}
	if len(required) == 0 {
		var plain []string
		for _, p := range props {
			if p.typ != "boolean" {
				plain = append(plain, p.name)
			}
		}
		if len(plain) == 1 {
			positional = plain
			use += " [" + plain[0] + "]"
		}
	}
	cmd.Use = use
	if cmd.Short == "" {
		cmd.Short = fmt.Sprintf("%s (module %s, scope %s)", spec.Type, module, spec.Scope)
	}
	cmd.Args = cobra.MaximumNArgs(len(positional))
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		out := map[string]json.RawMessage{}
		var order []string // arguments in the order given, for a readable log
		for i, v := range args {
			v, err := document(cmd, media[positional[i]], v)
			if err != nil {
				return err
			}
			j, err := convert(positional[i], types[positional[i]], v)
			if err != nil {
				return err
			}
			out[positional[i]] = j
			order = append(order, positional[i])
		}
		for _, p := range props {
			name, v := p.name, values[p.name]
			if v == nil || !cmd.Flags().Changed(strings.ReplaceAll(name, "_", "-")) {
				continue
			}
			if _, dup := out[name]; dup {
				return usageError{fmt.Errorf("%s given both as an argument and as --%s", name, name)}
			}
			doc, err := document(cmd, media[name], *v)
			if err != nil {
				return err
			}
			j, err := convert(name, types[name], doc)
			if err != nil {
				return err
			}
			out[name] = j
			order = append(order, name)
		}
		for _, p := range props {
			name, v := p.name, bools[p.name]
			if v != nil && cmd.Flags().Changed(strings.ReplaceAll(name, "_", "-")) {
				out[name], _ = json.Marshal(*v)
				order = append(order, name)
			}
		}
		for _, name := range secrets {
			v, err := readSecret(cmd, name)
			if err != nil {
				return err
			}
			out[name], _ = json.Marshal(v)
			order = append(order, name)
		}
		req := client.ActionRequest{Type: spec.Type}
		if len(out) > 0 {
			req.Args = orderedObject(order, out)
		}
		if expect >= 0 {
			v := uint64(expect)
			req.ExpectVersion = &v
		}
		return a.submit(cmd, req)
	}
}

// document reads an argument that is a document (an app file) from where
// it names: @path is a file, - standard input. Other values are the text.
func document(cmd *cobra.Command, isDoc bool, v string) (string, error) {
	switch {
	case !isDoc:
		return v, nil
	case v == "-":
		b, err := io.ReadAll(cmd.InOrStdin())
		return string(b), err
	case strings.HasPrefix(v, "@"):
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return "", usageError{err}
		}
		return string(b), nil
	}
	return v, nil
}

// convert turns a command-line string into the JSON the schema expects. A
// value of the wrong type is an invalid_args error, the same as when the
// server finds it, so scripts see one exit code either way.
func convert(name, typ, v string) (json.RawMessage, error) {
	switch typ {
	case "integer":
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "%s must be a whole number, not %q", name, v)
		}
		return json.Marshal(n)
	case "number":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "%s must be a number, not %q", name, v)
		}
		return json.Marshal(f)
	case "boolean":
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "%s must be true or false, not %q", name, v)
		}
		return json.Marshal(b)
	case "array":
		if strings.HasPrefix(strings.TrimSpace(v), "[") && json.Valid([]byte(v)) {
			return json.RawMessage(v), nil
		}
		return json.Marshal(strings.Split(v, ","))
	case "object":
		if !json.Valid([]byte(v)) {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "%s must be a JSON object", name)
		}
		return json.RawMessage(v), nil
	case "string":
		return json.Marshal(v)
	default: // untyped: JSON if it parses, else a string
		// "+5" and "-5" are relative changes (e.g. volume), not numbers.
		if (strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-")) && len(v) > 1 {
			return json.Marshal(v)
		}
		if json.Valid([]byte(v)) {
			return json.RawMessage(v), nil
		}
		return json.Marshal(v)
	}
}

// orderedObject writes a JSON object with its keys in the given order.
func orderedObject(keys []string, values map[string]json.RawMessage) json.RawMessage {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kj, _ := json.Marshal(k)
		b.Write(kj)
		b.WriteByte(':')
		b.Write(values[k])
	}
	b.WriteByte('}')
	return json.RawMessage(b.String())
}

// readSecret reads a secret argument: typed without echo on a terminal,
// or all of standard input otherwise (one trailing newline dropped).
func readSecret(cmd *cobra.Command, name string) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: ", name)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("reading %s from standard input: %w", name, err)
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if v == "" {
		return "", usageError{fmt.Errorf("%s is read from standard input, which was empty", name)}
	}
	return v, nil
}
