package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

// hostctl context: the saved logins.
func (a *app) contextCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "context [name]",
		Short: "List the saved logins, or choose the one hostctl uses",
		Long: `A machine can have a hostd for each of its users, and you may use several
machines. hostctl login ADDRESS --name NAME saves each under a name.

  hostctl context            list them; * marks the current one
  hostctl context NAME       use NAME from now on
  hostctl context default    use the login saved without a name
  hostctl --context NAME ... use NAME for this command only`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := readConfig()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				name := args[0]
				if name == "default" {
					name = ""
				} else if _, ok := c.Contexts[name]; !ok {
					return usageError{fmt.Errorf("no context %q (hostctl context lists them)", name)}
				}
				c.Current = name
				if _, err := saveConfig(c); err != nil {
					return err
				}
				fmt.Fprintf(a.stdout, "using %s\n", args[0])
				return nil
			}
			type row struct{ name, url string }
			var rows []row
			if c.URL != "" {
				rows = append(rows, row{"default", c.URL})
			}
			names := make([]string, 0, len(c.Contexts))
			for n := range c.Contexts {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				rows = append(rows, row{n, c.Contexts[n].URL})
			}
			if len(rows) == 0 {
				fmt.Fprintln(a.stdout, "no saved logins: hostctl login ADDRESS")
				return nil
			}
			return writeTable(a.stdout, tableStyle{}, []column[row]{
				{"", func(r row) string {
					if r.name == c.Current || (r.name == "default" && c.Current == "") {
						return "*"
					}
					return ""
				}},
				{"NAME", func(r row) string { return r.name }},
				{"ADDRESS", func(r row) string { return r.url }},
			}, rows)
		},
	}
}
