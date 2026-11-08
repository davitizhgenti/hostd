package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

// The commands for every kind of resource a module declares are the same:
//
//	hostctl get <plural> [name]     list them, or show one (its file, then its status)
//	hostctl <plural> [name]         the same
//	hostctl apply -f FILE|DIR|-     create or replace from files (--check: only check)
//	hostctl delete <kind> <name>
//	hostctl edit <kind> <name>      open the file in $EDITOR, then apply it
//
// They are generated from the manifests: a new kind has them at once.

// kindIndex finds a kind by its name or its plural.
type kindIndex struct {
	list []sdk.KindSpec
}

func (k kindIndex) find(word string) (sdk.KindSpec, bool) {
	for _, s := range k.list {
		if s.Kind == word || s.Plural == word {
			return s, true
		}
	}
	return sdk.KindSpec{}, false
}

func (k kindIndex) words() string {
	var out []string
	for _, s := range k.list {
		out = append(out, s.Plural)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func (a *app) addKindCommands(root *cobra.Command, manifests []sdk.Manifest) {
	var idx kindIndex
	for _, m := range manifests {
		idx.list = append(idx.list, m.Kinds...)
	}
	if len(idx.list) == 0 {
		return
	}
	var get *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "get" {
			get = c
		}
	}
	if get == nil {
		get = &cobra.Command{Use: "get", Short: "Show what hostd has", Args: cobra.ArbitraryArgs, RunE: groupRun}
		root.AddCommand(get)
	}
	taken := func(name string) bool {
		for _, c := range root.Commands() {
			if c.Name() == name {
				return true
			}
		}
		return false
	}
	for _, k := range idx.list {
		short := "List the " + k.Plural + ", or show one"
		if k.Description != "" {
			short = k.Description + ": list, or show one"
		}
		for _, parent := range []*cobra.Command{get, nil} {
			if parent == nil {
				if taken(k.Plural) {
					continue
				}
				parent = root
			}
			cmd := &cobra.Command{Use: k.Plural + " [name]", Short: short, Args: cobra.MaximumNArgs(1)}
			cmd.RunE = func(cmd *cobra.Command, args []string) error { return a.getKind(cmd.Context(), k, args) }
			parent.AddCommand(cmd)
		}
	}
	root.AddCommand(a.applyCommand(idx), a.deleteCommand(idx), a.editCommand(idx))
}

func (a *app) getKind(ctx context.Context, k sdk.KindSpec, args []string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	if len(args) == 1 {
		it, err := c.Resource(ctx, k.Plural, args[0])
		if err != nil {
			return err
		}
		if a.jsonOut {
			return a.printJSON(it)
		}
		file, err := c.ResourceFile(ctx, k.Plural, args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "%s", file)
		if len(file) > 0 && file[len(file)-1] != '\n' {
			fmt.Fprintln(a.stdout)
		}
		fmt.Fprintf(a.stdout, "# %s, generation %d\n", describeStatus(it), it.Generation)
		if it.Error != "" {
			fmt.Fprintf(a.stdout, "# the file is not in use: %s\n", it.Error)
		}
		return nil
	}
	if len(k.Columns) > 0 && !a.jsonOut {
		var raw json.RawMessage
		if err := c.Get(ctx, "/v1/"+k.Plural, &raw); err != nil {
			return err
		}
		return a.printTable(&sdk.CLI{Columns: k.Columns, Empty: k.Empty}, raw)
	}
	items, err := c.Resources(ctx, k.Plural)
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(items)
	}
	return writeTable(a.stdout, tableStyle{}, []column[client.Resource]{
		{"NAME", func(r client.Resource) string { return r.Name }},
		{"STATUS", describeStatus},
		{"PROBLEM", func(r client.Resource) string { return oneLine(r.Error) }},
	}, items)
}

// describeStatus is a resource's state in a word: ready, rejected (the file
// is not in use), or the first condition that is not True.
func describeStatus(r client.Resource) string {
	switch {
	case r.Error != "" && r.Spec == nil:
		return "rejected"
	case r.Error != "":
		return "rejected, previous in use"
	case r.Status.Ready():
		return "ready"
	}
	for _, c := range r.Status.Conditions {
		if c.Status != sdk.ConditionTrue {
			return strings.ToLower(c.Type) + " " + strings.ToLower(c.Status)
		}
	}
	return "in use"
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

// resourceFile is one file to apply: its kind, its name and its content.
type resourceFile struct {
	kind sdk.KindSpec
	name string
	body []byte
	from string
}

func (a *app) applyCommand(idx kindIndex) *cobra.Command {
	var files []string
	var kind, name string
	var check bool
	cmd := &cobra.Command{
		Use:   "apply -f FILE|DIR|-",
		Short: "Create or replace resources from files",
		Long: `Create or replace resources from TOML files (JSON objects work too).

A file's kind is its folder's name (rules/night.toml is the rule "night"), or
--kind; its name is the file name without .toml, or --name. A folder holding
folders named after kinds (a copy of ~/.config/hostd) applies them all. "-"
reads one resource from standard input and needs --kind and --name.

A file a module rejects is reported and not written; the others are still
applied, and hostctl exits with an error. With --check nothing is applied or
written: it says whether each would be accepted.

kinds: ` + idx.words(),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(files) == 0 {
				return usageError{errors.New("apply needs -f FILE, DIR or -")}
			}
			var all []resourceFile
			for _, f := range files {
				got, err := a.collect(idx, f, kind, name)
				if err != nil {
					return usageError{err}
				}
				all = append(all, got...)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			failed := 0
			for _, f := range all {
				_, err := c.Apply(cmd.Context(), f.kind.Plural, f.name, f.body, check)
				switch {
				case err != nil:
					failed++
					fmt.Fprintf(a.stdout, "%s/%s: rejected: %s\n", f.kind.Kind, f.name, oneLine(err.Error()))
				case check:
					fmt.Fprintf(a.stdout, "%s/%s: would be accepted\n", f.kind.Kind, f.name)
				default:
					fmt.Fprintf(a.stdout, "%s/%s: applied\n", f.kind.Kind, f.name)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d resources rejected", failed, len(all))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&files, "filename", "f", nil, "a file, a folder, or - for standard input (repeat for more)")
	cmd.Flags().StringVar(&kind, "kind", "", "the kind, when the folder does not say")
	cmd.Flags().StringVar(&name, "name", "", "the name, when the file name does not say")
	cmd.Flags().BoolVar(&check, "check", false, "only check that each would be accepted")
	return cmd
}

// collect reads the files named by one -f.
func (a *app) collect(idx kindIndex, path, kindFlag, nameFlag string) ([]resourceFile, error) {
	flagKind := sdk.KindSpec{}
	if kindFlag != "" {
		k, ok := idx.find(kindFlag)
		if !ok {
			return nil, fmt.Errorf("no kind %q (kinds: %s)", kindFlag, idx.words())
		}
		flagKind = k
	}
	if path == "-" {
		if kindFlag == "" || nameFlag == "" {
			return nil, errors.New("reading standard input needs --kind and --name")
		}
		b, err := io.ReadAll(a.stdin)
		if err != nil {
			return nil, err
		}
		return []resourceFile{{kind: flagKind, name: nameFlag, body: b, from: "standard input"}}, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	read := func(k sdk.KindSpec, file, name string) (resourceFile, error) {
		b, err := os.ReadFile(file)
		return resourceFile{kind: k, name: name, body: b, from: file}, err
	}
	if !fi.IsDir() {
		k := flagKind
		if kindFlag == "" {
			var ok bool
			if k, ok = idx.find(filepath.Base(filepath.Dir(path))); !ok {
				return nil, fmt.Errorf("%s: which kind? Put it in a folder named after one (%s) or give --kind", path, idx.words())
			}
		}
		name := nameFlag
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(path), ".toml")
		}
		f, err := read(k, path, name)
		return []resourceFile{f}, err
	}
	// A folder: its own files if it is named after a kind (or --kind), else
	// the kind folders in it.
	var out []resourceFile
	tomls := func(dir string, k sdk.KindSpec) error {
		list, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, de := range list {
			n := de.Name()
			if de.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".toml") {
				continue
			}
			f, err := read(k, filepath.Join(dir, n), strings.TrimSuffix(n, ".toml"))
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return nil
	}
	if k, ok := idx.find(filepath.Base(filepath.Clean(path))); ok || kindFlag != "" {
		if kindFlag != "" {
			k = flagKind
		}
		return out, tomls(path, k)
	}
	for _, k := range idx.list {
		dir := filepath.Join(path, k.Plural)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			if err := tomls(dir, k); err != nil {
				return nil, err
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no resource files (a folder named after a kind, or holding such folders: %s)", path, idx.words())
	}
	return out, nil
}

func (a *app) deleteCommand(idx kindIndex) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <kind> <name>",
		Short: "Delete a resource (its file, and it goes out of use)",
		Long:  "Delete a resource. kinds: " + idx.words(),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, ok := idx.find(args[0])
			if !ok {
				return usageError{fmt.Errorf("no kind %q (kinds: %s)", args[0], idx.words())}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.DeleteResource(cmd.Context(), k.Plural, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "%s/%s: deleted\n", k.Kind, args[1])
			return nil
		},
	}
}

func (a *app) editCommand(idx kindIndex) *cobra.Command {
	return &cobra.Command{
		Use:   "edit <kind> <name>",
		Short: "Change a resource in $EDITOR; it is applied when you save and quit",
		Long: `Open a resource's file in $VISUAL or $EDITOR (default vi). When the editor
ends, the file is applied. If a module rejects it, the error is shown and the
edited copy is kept, so nothing is lost. kinds: ` + idx.words(),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, ok := idx.find(args[0])
			if !ok {
				return usageError{fmt.Errorf("no kind %q (kinds: %s)", args[0], idx.words())}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			before, err := c.ResourceFile(cmd.Context(), k.Plural, args[1])
			if err != nil {
				return err
			}
			dir, err := os.MkdirTemp("", "hostctl-edit-")
			if err != nil {
				return err
			}
			file := filepath.Join(dir, args[1]+".toml")
			if err := os.WriteFile(file, before, 0o600); err != nil {
				return err
			}
			editor := firstSet(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
			parts := strings.Fields(editor)
			ed := exec.CommandContext(cmd.Context(), parts[0], append(parts[1:], file)...)
			ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := ed.Run(); err != nil {
				return fmt.Errorf("the editor failed (your copy is in %s): %w", file, err)
			}
			after, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if string(after) == string(before) {
				_ = os.RemoveAll(dir)
				fmt.Fprintf(a.stdout, "%s/%s: no changes\n", k.Kind, args[1])
				return nil
			}
			if _, err := c.Apply(cmd.Context(), k.Plural, args[1], after, false); err != nil {
				return fmt.Errorf("rejected (your copy is in %s): %w", file, err)
			}
			_ = os.RemoveAll(dir)
			fmt.Fprintf(a.stdout, "%s/%s: applied\n", k.Kind, args[1])
			return nil
		},
	}
}

func firstSet(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
