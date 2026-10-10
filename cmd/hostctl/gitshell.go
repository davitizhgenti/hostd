package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// gitShellCommand is the forced command of SSH keys that may only push
// (deploy.authorize): sshd runs it instead of what the client asked for,
// which is in SSH_ORIGINAL_COMMAND. Only git push and fetch to a
// service's repository (~/hostd/git/<app>.git) get through.
func (a *app) gitShellCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "git-shell",
		Short:  "Allow only git push and fetch to a service's repository (an SSH forced command)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			root := os.Getenv("HOSTD_ROOT")
			if root == "" {
				root = filepath.Join(home, "hostd")
			}
			verb, repo, err := gitShellAllow(os.Getenv("SSH_ORIGINAL_COMMAND"), home, filepath.Join(root, "git"))
			if err != nil {
				return usageError{err}
			}
			git, err := exec.LookPath("git")
			if err != nil {
				return err
			}
			return syscall.Exec(git, []string{"git", verb, repo}, os.Environ())
		},
	}
}

// gitShellAllow checks a git client's command (git-receive-pack '<path>')
// and returns git's subcommand and the repository's real path.
func gitShellAllow(command, home, gitDir string) (verb, repo string, err error) {
	refuse := errors.New("this key may only git push to (or fetch from) a hostd service's repository")
	name, arg, ok := strings.Cut(strings.TrimSpace(command), " ")
	switch name {
	case "git-receive-pack":
		verb = "receive-pack"
	case "git-upload-pack":
		verb = "upload-pack"
	default:
		return "", "", refuse
	}
	if !ok {
		return "", "", refuse
	}
	path := strings.TrimSpace(arg)
	if len(path) >= 2 && path[0] == '\'' && path[len(path)-1] == '\'' {
		path = path[1 : len(path)-1]
	}
	if path == "" || strings.ContainsAny(path, "'\"\\\n") {
		return "", "", refuse
	}
	switch {
	case strings.HasPrefix(path, "~/"):
		path = filepath.Join(home, path[2:])
	case !filepath.IsAbs(path):
		path = filepath.Join(home, path)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", "", fmt.Errorf("no repository %s", path)
	}
	dir, err := filepath.EvalSymlinks(gitDir)
	if err != nil || filepath.Dir(real) != dir || !strings.HasSuffix(real, ".git") {
		return "", "", refuse
	}
	return verb, real, nil
}
