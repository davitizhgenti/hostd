package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/sdk"
)

// readParams lists the {name} parameters of a read's path, in order.
func readParams(path string) []string {
	var out []string
	for rest := path; ; {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return out
		}
		out = append(out, rest[i+1:i+j])
		rest = rest[i+j+1:]
	}
}

// addReadCommands adds "hostctl get <module> <read>" for every read the
// server declares, and the top-level aliases that manifests ask for (for
// actions as well as reads). A name already taken is left alone: built-in
// commands and earlier modules win.
func (a *app) addReadCommands(root *cobra.Command, manifests []sdk.Manifest) {
	get := &cobra.Command{
		Use:   "get",
		Short: "Show what a module reports (hostctl get <module> <read>)",
		Long: `Show what a module reports: the reads its manifest declares. A read that
returns a list prints a table when the module says how; otherwise, and with
--json, the JSON as the server sent it. A read with {parameters} in its path
takes them as arguments; ?query parameters are given as --param key=value.`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRun,
	}
	taken := func(name string) bool {
		return slices.ContainsFunc(root.Commands(), func(c *cobra.Command) bool { return c.Name() == name || slices.Contains(c.Aliases, name) })
	}
	type alias struct {
		name string
		make func(*cobra.Command)
	}
	var aliases []alias
	for _, m := range manifests {
		var group *cobra.Command
		for _, rd := range m.Reads {
			if group == nil {
				group = child(get, m.Name, "What "+m.Name+" reports")
			}
			leaf := child(group, rd.Name, "")
			a.makeReadCommand(leaf, rd, m.Name)
			if rd.CLI != nil {
				for _, name := range rd.CLI.Alias {
					aliases = append(aliases, alias{name, func(c *cobra.Command) { a.makeReadCommand(c, rd, m.Name) }})
				}
			}
		}
		for _, spec := range m.Actions {
			if spec.CLI != nil {
				for _, name := range spec.CLI.Alias {
					aliases = append(aliases, alias{name, func(c *cobra.Command) { a.makeActionCommand(c, spec, m.Name) }})
				}
			}
		}
	}
	if len(get.Commands()) > 0 && !taken("get") {
		root.AddCommand(get)
	}
	for _, al := range aliases {
		if taken(al.name) {
			continue
		}
		c := &cobra.Command{Use: al.name, Args: cobra.ArbitraryArgs}
		al.make(c)
		root.AddCommand(c)
	}
}

func (a *app) makeReadCommand(cmd *cobra.Command, rd sdk.ReadSpec, module string) {
	params := readParams(rd.Path)
	use := cmd.Name()
	for _, p := range params {
		use += " <" + p + ">"
	}
	cmd.Use = use
	cmd.Short = rd.Description
	if cmd.Short == "" {
		cmd.Short = fmt.Sprintf("GET %s (module %s)", rd.Path, module)
	}
	cmd.Args = cobra.ExactArgs(len(params))
	var query []string
	cmd.Flags().StringArrayVar(&query, "param", nil, "a query parameter, key=value (repeat for more)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		path := rd.Path
		for i, p := range params {
			path = strings.Replace(path, "{"+p+"}", url.PathEscape(args[i]), 1)
		}
		if len(query) > 0 {
			q := url.Values{}
			for _, kv := range query {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return usageError{fmt.Errorf("--param %q: use key=value", kv)}
				}
				q.Add(k, v)
			}
			path += "?" + q.Encode()
		}
		c, err := a.client()
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if err := c.Get(cmd.Context(), path, &raw); err != nil {
			return err
		}
		if a.jsonOut || rd.CLI == nil || len(rd.CLI.Columns) == 0 {
			return a.printJSON(raw)
		}
		return a.printTable(rd.CLI, raw)
	}
}

// printTable prints a read that returned a list of objects as the table the
// module described. Anything else is printed as JSON.
func (a *app) printTable(cli *sdk.CLI, raw json.RawMessage) error {
	var rows []map[string]any
	var whole any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 4242 stays 4242
	if err := dec.Decode(&whole); err != nil {
		return a.printJSON(raw)
	}
	list := whole
	if cli.Rows != "" { // the list is a field of the answer
		for _, key := range strings.Split(cli.Rows, ".") {
			m, _ := list.(map[string]any)
			list = m[key]
		}
	}
	if list != nil {
		items, ok := list.([]any)
		if !ok {
			return a.printJSON(raw)
		}
		for _, it := range items {
			row, ok := it.(map[string]any)
			if !ok {
				return a.printJSON(raw)
			}
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 && cli.Empty != "" {
		_, err := fmt.Fprintln(a.stdout, cli.Empty)
		return err
	}
	cols := make([]column[map[string]any], len(cli.Columns))
	for i, c := range cli.Columns {
		cols[i] = column[map[string]any]{c.Title, func(row map[string]any) string { return cell(row, c) }}
	}
	return writeTable(a.stdout, tableStyle{}, cols, rows)
}

// cell is one value of a row, written as its column says. A value that is
// missing, empty or null is "-".
func cell(row map[string]any, c sdk.Column) string {
	for _, path := range strings.Split(c.Path, "|") {
		var v any = row
		for _, key := range strings.Split(path, ".") {
			m, ok := v.(map[string]any)
			if !ok {
				v = nil
				break
			}
			v = m[key]
		}
		if s := render(v, c.Format); s != "" {
			if c.Max > 0 {
				s = shorten(s, c.Max)
			}
			return s
		}
	}
	return "-"
}

// render writes one JSON value as text; "" means there is nothing.
func render(v any, format string) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		if format == "time" {
			if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
				return t.In(displayZone).Format("15:04:05")
			}
		}
		return x
	case json.Number:
		return x.String()
	case bool:
		return fmt.Sprint(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			if s := render(e, ""); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		b, _ := json.Marshal(x) // object keys come out sorted
		return string(b)
	}
	return fmt.Sprint(v)
}
