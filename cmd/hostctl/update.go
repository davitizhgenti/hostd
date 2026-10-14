package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- update -------------------------------------------------------------------

func (a *app) updateCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "update", Short: "Update hostd on the machine", Args: cobra.ArbitraryArgs, RunE: groupRun}
	var sshTarget string
	push := &cobra.Command{
		Use:   "push [binary]",
		Short: "Install a hostd build on the machine and restart into it (rolled back if it does not start)",
		Long: `Install a hostd build on the machine and restart into it.

Without a binary, builds hostd from the source checkout you are in, for the
machine's architecture (needs Go). The machine keeps the previous version:
if the new one does not start, systemd switches back to it on its own.

--ssh user@machine copies the binary over SSH and installs it there, for
when the running hostd is too broken to take an upload.`,
		Example: `  hostctl update push
  hostctl update push ./dist/hostd-linux-amd64
  hostctl update push --ssh screen@192.168.1.20`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil && sshTarget == "" {
				return err
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			} else {
				arch := "amd64"
				if c != nil {
					if v, err := c.Version(cmd.Context()); err == nil {
						arch = v.Arch
					}
				}
				if path, err = buildHostd(cmd.Context(), arch, a.stderr); err != nil {
					return err
				}
				defer os.Remove(path)
			}
			if sshTarget != "" {
				return a.pushOverSSH(cmd.Context(), sshTarget, path)
			}
			return a.pushOverAPI(cmd.Context(), c, path)
		},
	}
	push.Flags().StringVar(&sshTarget, "ssh", "", "install over SSH instead of the API, e.g. screen@192.168.1.20")
	cmd.AddCommand(push)
	return cmd
}

// buildHostd builds hostd for linux/<arch> from the checkout in the
// current directory.
func buildHostd(ctx context.Context, arch string, stderr io.Writer) (string, error) {
	if _, err := os.Stat("cmd/hostd"); err != nil {
		return "", usageError{errors.New("no binary given and not in a hostd source checkout (cmd/hostd not found)")}
	}
	ver := "dev"
	if out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output(); err == nil {
		ver = strings.TrimSpace(string(out)) + "-dev"
	}
	out, err := os.CreateTemp("", "hostd-push-*")
	if err != nil {
		return "", err
	}
	out.Close()
	fmt.Fprintf(stderr, "building hostd %s for linux/%s...\n", ver, arch)
	build := exec.CommandContext(ctx, "go", "build", "-trimpath",
		"-ldflags", "-X github.com/davitizhgenti/hostd/internal/version.Version="+ver,
		"-o", out.Name(), "./cmd/hostd")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	build.Stdout, build.Stderr = stderr, stderr
	if err := build.Run(); err != nil {
		os.Remove(out.Name())
		return "", fmt.Errorf("go build: %w", err)
	}
	return out.Name(), nil
}

func (a *app) pushOverAPI(ctx context.Context, c *client.Client, path string) error {
	before, err := c.Version(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(a.stderr, "uploading %s...\n", path)
	res, err := c.Update(ctx, f)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "installed hostd %s (was %s); restarting\n", res.Version, before.Version)

	// Wait for the new version to answer, or for systemd to roll back.
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		v, err := c.Version(ctx)
		if err != nil {
			continue // restarting
		}
		switch {
		case v.Version == res.Version:
			fmt.Fprintf(a.stdout, "hostd %s is running\n", v.Version)
			return nil
		case v.RolledBackFrom == res.Version:
			return &client.Error{Err: sdk.Errorf(sdk.CodeInternal,
				"hostd %s did not start; systemd rolled back to %s", res.Version, v.Version)}
		}
	}
	return &client.Error{Err: sdk.Errorf(sdk.CodeTimeout, "hostd did not come back within 3 minutes")}
}

func (a *app) pushOverSSH(ctx context.Context, target, path string) error {
	remote := "/tmp/hostd-push"
	fmt.Fprintf(a.stderr, "copying %s to %s...\n", path, target)
	scp := exec.CommandContext(ctx, "scp", "-q", path, target+":"+remote)
	scp.Stdout, scp.Stderr = a.stderr, a.stderr
	if err := scp.Run(); err != nil {
		return fmt.Errorf("scp: %w", err)
	}
	run := exec.CommandContext(ctx, "ssh", target,
		"chmod +x "+remote+" && ~/.local/lib/hostd/current/hostd -install "+remote+"; status=$?; rm -f "+remote+"; exit $status")
	run.Stdout, run.Stderr = a.stdout, a.stderr
	return run.Run()
}
