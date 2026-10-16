package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/internal/tlsca"
	"github.com/davitizhgenti/hostd/internal/version"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- login ----------------------------------------------------------------

func (a *app) loginCommand() *cobra.Command {
	var tokenFile, pin string
	cmd := &cobra.Command{
		Use:   "login <address>",
		Short: "Save the address and token of a hostd machine",
		Long: `Save the address and token of a hostd machine in ~/.config/hostctl.

The address is host or host:port (HTTPS, port 7300) or unix:///path/to/hostd.sock.
Over HTTPS, the machine's certificate authority is shown first: compare its
fingerprint with what hostctl tls prints on the machine (ssh <machine>
hostctl tls), then confirm. hostctl trusts that CA, and only it, from then
on. --ca-fingerprint gives it instead, for scripts.

The token is read from --token-file, or from standard input: paste the
admin token that hostd printed on its first start, or one made with
hostctl token create.`,
		Example: `  hostctl login core
  hostctl login 192.168.18.33:7300 --ca-fingerprint 1f2e3d4c... --token-file token.txt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr := loginAddress(args[0])
			in := bufio.NewReader(a.stdin)
			terminal := false
			if f, ok := a.stdin.(*os.File); ok && isTerminal(f) {
				terminal = true
			}
			var opts []client.Option
			if strings.HasPrefix(addr, "https://") {
				fp, err := a.trustCA(cmd, addr, pin, in, terminal)
				if err != nil {
					return err
				}
				pin = fp
				if pin != "" {
					opts = append(opts, client.PinCA(pin))
				}
			}
			var token string
			if tokenFile != "" {
				b, err := os.ReadFile(tokenFile)
				if err != nil {
					return err
				}
				token = strings.TrimSpace(string(b))
			} else {
				if terminal {
					fmt.Fprint(a.stderr, "Token: ")
				}
				line, err := in.ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				token = strings.TrimSpace(line)
			}
			if token == "" {
				return usageError{errors.New("no token given")}
			}
			c, err := client.New(addr, token, opts...)
			if err != nil {
				return usageError{err}
			}
			v, err := c.Version(cmd.Context())
			if err != nil {
				return err
			}
			path, err := saveConfig(Config{URL: addr, Token: token, CAFingerprint: pin})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "Logged in to hostd %s at %s (saved in %s)\n", v.Version, addr, path)
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the token from this file")
	cmd.Flags().StringVar(&pin, "ca-fingerprint", "", "the machine's CA fingerprint (hostctl tls on the machine), instead of confirming it")
	return cmd
}

// loginAddress completes an address: HTTPS, and port 7300 if none.
func loginAddress(addr string) string {
	if strings.HasPrefix(addr, "unix://") {
		return addr
	}
	scheme := "https://"
	if s, rest, ok := strings.Cut(addr, "://"); ok {
		scheme, addr = s+"://", rest
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), "7300")
	}
	return scheme + addr
}

// trustCA reads the CA the machine presents and has it confirmed (or
// checks it against want). It returns the fingerprint to pin, or "" for a
// machine with a certificate from a public CA (Tailscale's, Let's
// Encrypt's), which the system's CAs check instead. Nothing is sent to
// the machine before this.
func (a *app) trustCA(cmd *cobra.Command, addr, want string, in *bufio.Reader, terminal bool) (string, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", usageError{err}
	}
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // only to read the CA, which is then confirmed
	conn, err := d.DialContext(cmd.Context(), "tcp", u.Host)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s over HTTPS: %w", u.Host, err)
	}
	cs := conn.(*tls.Conn).ConnectionState()
	conn.Close()
	ca, err := tlsca.ServerCA(cs)
	if err != nil {
		return "", nil //nolint:nilerr // no CA in the chain: a public certificate, checked by the system's CAs
	}
	got := tlsca.Fingerprint(ca.Raw)
	if want != "" {
		if norm := strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(want)); norm != got {
			return "", fmt.Errorf("the machine's CA is %s, not the one given: another machine, or someone in between", tlsca.Pretty(got))
		}
		return got, nil
	}
	if !terminal {
		return "", usageError{fmt.Errorf("the machine's CA is %s: confirm it with --ca-fingerprint (hostctl tls on the machine shows it)", got)}
	}
	fmt.Fprintf(a.stderr, "The machine's certificate authority (%s):\n\n    %s\n\nCompare it with what the machine shows (ssh <machine> hostctl tls).\nTrust it? [y/N] ",
		ca.Subject.CommonName, tlsca.Pretty(got))
	line, _ := in.ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return "", errors.New("not trusted; nothing was sent")
	}
	return got, nil
}

// --- tls ------------------------------------------------------------------

func (a *app) tlsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tls",
		Short: "Show the machine's certificate authority (to pin or install) and its certificate",
		Long: `Show the fingerprint of the machine's certificate authority, which
hostctl login asks to confirm, and the certificate the API serves.

Installing the CA (ca.pem) on a laptop or phone lets browsers open
https://<machine>:7300/docs/ and the add-ons' sites without warnings. The
CA can only vouch for this machine's names and private addresses.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var info struct {
				Fingerprint string     `json:"fingerprint"`
				Leaf        tlsca.Leaf `json:"leaf"`
			}
			if err := c.Get(cmd.Context(), "/v1/tls", &info); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(info)
			}
			if info.Fingerprint == "" {
				_, err := fmt.Fprintln(a.stdout, "the API is plain HTTP here ([api] tls = false)")
				return err
			}
			fmt.Fprintf(a.stdout, "Certificate authority (pin):\n    %s\n", tlsca.Pretty(info.Fingerprint))
			fmt.Fprintf(a.stdout, "    file: GET /v1/tls/ca.pem, or ~/.local/state/hostd/tls/ca.pem on the machine\n")
			fmt.Fprintf(a.stdout, "Certificate: %s\n    valid until %s\n",
				strings.Join(append(append([]string{}, info.Leaf.Names...), info.Leaf.IPs...), ", "), info.Leaf.NotAfter.In(displayZone).Format("2006-01-02"))
			if len(info.Leaf.Skipped) > 0 {
				fmt.Fprintf(a.stdout, "Left out: %s (the CA was made before they were configured; to remake it, stop hostd, remove ~/.local/state/hostd/tls, start it, and log in again everywhere)\n",
					strings.Join(info.Leaf.Skipped, ", "))
			}
			return nil
		},
	}
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
