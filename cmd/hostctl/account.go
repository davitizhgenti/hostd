package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/internal/version"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- login ----------------------------------------------------------------

func (a *app) loginCommand() *cobra.Command {
	var tokenFile string
	cmd := &cobra.Command{
		Use:   "login <address>",
		Short: "Save the address and token of a hostd machine",
		Long: `Save the address and token of a hostd machine in ~/.config/hostctl.

The address is host:port (port 7300) or unix:///path/to/hostd.sock. The token
is read from --token-file, or from standard input: paste the admin token
that hostd printed on its first start, or one made with hostctl token create.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr := args[0]
			if !strings.Contains(addr, "://") && !strings.Contains(addr, ":") {
				addr += ":7300"
			}
			var token string
			if tokenFile != "" {
				b, err := os.ReadFile(tokenFile)
				if err != nil {
					return err
				}
				token = strings.TrimSpace(string(b))
			} else {
				if f, ok := a.stdin.(*os.File); ok && isTerminal(f) {
					fmt.Fprint(a.stderr, "Token: ")
				}
				line, err := bufio.NewReader(a.stdin).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				token = strings.TrimSpace(line)
			}
			if token == "" {
				return usageError{errors.New("no token given")}
			}
			c, err := client.New(addr, token)
			if err != nil {
				return usageError{err}
			}
			v, err := c.Version(cmd.Context())
			if err != nil {
				return err
			}
			path, err := saveConfig(Config{URL: addr, Token: token})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "Logged in to hostd %s at %s (saved in %s)\n", v.Version, addr, path)
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the token from this file")
	return cmd
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// --- version --------------------------------------------------------------

func (a *app) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the versions of hostctl and the connected hostd",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(a.stdout, "hostctl %s\n", version.Version)
			c, err := a.client()
			if err != nil {
				return err
			}
			v, err := c.Version(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(v)
			}
			dev := ""
			if v.Dev {
				dev = " (dev build)"
			}
			fmt.Fprintf(a.stdout, "hostd   %s%s, %s/%s\n", v.Version, dev, v.OS, v.Arch)
			return nil
		},
	}
}

// --- tokens ---------------------------------------------------------------

func (a *app) tokenCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Create, list and revoke API tokens", Args: cobra.ArbitraryArgs, RunE: groupRun}

	var scopes []string
	var kind, ttl string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a token; its secret is shown only once",
		Example: `  hostctl token create phone --scopes read,apps,audio
  hostctl token create backup.sh --kind automation --scopes read,services`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			t, err := c.CreateToken(cmd.Context(), client.TokenRequest{
				Name: args[0], Kind: sdk.SourceKind(kind), Scopes: scopes, TTL: ttl})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(t)
			}
			fmt.Fprintf(a.stdout, "Created token %q (%s, scopes %s). Its secret, shown only now:\n\n    %s\n",
				t.Name, t.Kind, strings.Join(t.Scopes, ","), t.Secret)
			return nil
		},
	}
	create.Flags().StringSliceVar(&scopes, "scopes", nil, "scopes, comma-separated (required)")
	create.Flags().StringVar(&kind, "kind", "manual", "manual (a person's device), automation (scripts) or local (the on-screen menu)")
	create.Flags().StringVar(&ttl, "ttl", "", "lifetime such as 1h; default never expires")
	_ = create.MarkFlagRequired("scopes")

	list := &cobra.Command{
		Use:   "list",
		Short: "List tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var toks []client.Token
			if done, err := a.fetch(cmd, "/v1/tokens", &toks); done || err != nil {
				return err
			}
			return writeTable(a.stdout, tableStyle{}, tokenColumns(time.Now()), toks)
		},
	}

	revoke := &cobra.Command{
		Use:   "revoke <name or id>",
		Short: "Revoke a token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id := args[0]
			if !strings.HasPrefix(id, "tok_") {
				toks, err := c.Tokens(cmd.Context())
				if err != nil {
					return err
				}
				id = ""
				for _, t := range toks {
					if t.Name == args[0] && t.Revoked == nil {
						id = t.ID
					}
				}
				if id == "" {
					return &client.Error{Err: sdk.Errorf(sdk.CodeNotFound, "no active token named %q", args[0])}
				}
			}
			if err := c.RevokeToken(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "Revoked %s\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(create, list, revoke)
	return cmd
}

// tokenColumns are the columns of hostctl token list; now decides which
// tokens have expired.
func tokenColumns(now time.Time) []column[client.Token] {
	stamp := func(t time.Time) string { return t.In(displayZone).Format("2006-01-02 15:04") }
	return []column[client.Token]{
		{"NAME", func(t client.Token) string { return t.Name }},
		{"KIND", func(t client.Token) string { return string(t.Kind) }},
		{"SCOPES", func(t client.Token) string { return strings.Join(t.Scopes, ",") }},
		{"LAST USED", func(t client.Token) string {
			if t.LastUsed == nil {
				return "never"
			}
			return stamp(*t.LastUsed)
		}},
		{"STATE", func(t client.Token) string {
			switch {
			case t.Revoked != nil:
				return "revoked"
			case t.Expires != nil && !now.Before(*t.Expires):
				return "expired"
			case t.Expires != nil:
				return "expires " + stamp(*t.Expires)
			}
			return "active"
		}},
		{"ID", func(t client.Token) string { return t.ID }},
	}
}
