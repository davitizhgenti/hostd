package deploy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// repo is a service's bare repository, the target of git push.
func (m *Module) repo(app string) string {
	return filepath.Join(m.opts.Root, "git", app+".git")
}

func (m *Module) git(ctx context.Context, args ...string) (string, error) {
	return m.gitWith(ctx, nil, args...)
}

// gitWith runs git with env (nil: hostd's own).
func (m *Module) gitWith(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// hook deploys a push to the service's branch: hostctl deploy run, whose
// output reaches the pusher.
const hook = `#!/bin/sh
# Managed by hostd: a push to %[2]s deploys %[1]s.
while read -r old new ref; do
	[ "$ref" = "refs/heads/%[2]s" ] || continue
	echo "hostd: deploying %[1]s at $new"
	exec hostctl deploy run %[1]s --rev "$new"
done
`

// initRepo creates the bare repository and its hook (again: the hook is
// rewritten, the repository kept).
func (m *Module) initRepo(ctx context.Context, app, branch string) (string, error) {
	dir := m.repo(app)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if _, err := m.git(ctx, "init", "--bare", "--initial-branch="+branch, dir); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks", "post-receive"), []byte(fmt.Sprintf(hook, app, branch)), 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// export writes rev's tree into a new release directory and returns the
// full commit.
func (m *Module) export(ctx context.Context, app, rev, branch string) (sha, dir string, err error) {
	repo := m.repo(app)
	if rev == "" {
		rev = branch
	}
	sha, err = m.git(ctx, "--git-dir", repo, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("no commit %q in %s: %w", rev, repo, err)
	}
	dir = filepath.Join(m.opts.Root, "releases", app, sha[:12]+"-"+m.opts.Clock.Now().UTC().Format("20060102T150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	// git archive | tar -x, through an OS pipe both processes hold.
	pr, pw, err := os.Pipe()
	if err != nil {
		return "", "", err
	}
	archive := exec.CommandContext(ctx, "git", "--git-dir", repo, "archive", "--format=tar", sha)
	untar := exec.CommandContext(ctx, "tar", "-x", "-C", dir)
	var archiveErr, untarErr bytes.Buffer
	archive.Stdout, archive.Stderr = pw, &archiveErr
	untar.Stdin, untar.Stderr = pr, &untarErr
	if err := untar.Start(); err != nil {
		pr.Close()
		pw.Close()
		return "", "", err
	}
	pr.Close() // tar has it
	aerr := archive.Run()
	pw.Close() // tar sees the end
	uerr := untar.Wait()
	if aerr != nil {
		return "", "", fmt.Errorf("git archive: %w: %s", aerr, strings.TrimSpace(archiveErr.String()))
	}
	if uerr != nil {
		return "", "", fmt.Errorf("tar: %w: %s", uerr, strings.TrimSpace(untarErr.String()))
	}
	return sha, dir, nil
}

// build runs the service's build command in the release, logging to
// .hostd-build.log there. A failure carries the log's end.
func (m *Module) build(ctx context.Context, dir string, argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	log, err := os.Create(filepath.Join(dir, ".hostd-build.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var tail bytes.Buffer
	cmd.Stdout = &teeTail{w: log, tail: &tail}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build (%s) failed: %w\n%s", strings.Join(argv, " "), err, lastLines(tail.String(), 20))
	}
	return nil
}

// teeTail writes to w and keeps the output for the error message.
type teeTail struct {
	w    *os.File
	tail *bytes.Buffer
}

func (t *teeTail) Write(p []byte) (int, error) {
	t.tail.Write(p)
	if t.tail.Len() > 64<<10 {
		t.tail.Next(t.tail.Len() - 64<<10)
	}
	return t.w.Write(p)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
