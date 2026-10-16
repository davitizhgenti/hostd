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

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/internal/tlsca"
	"github.com/davitizhgenti/hostd/internal/version"
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
		Example: `  hostctl login core.local
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
	conn, err := d.DialContext(cmd.Context(), "tcp", client.ResolveLocal(cmd.Context(), u.Host))
	if err != nil {
		if dnsErr := (*net.DNSError)(nil); errors.As(err, &dnsErr) && dnsErr.IsNotFound && !strings.Contains(u.Hostname(), ".") {
			// Often an SSH alias, which only ssh knows.
			return "", fmt.Errorf("cannot reach %s: the network does not know the name %q; try %s.local or the machine's address", u.Host, u.Hostname(), u.Hostname())
		}
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
