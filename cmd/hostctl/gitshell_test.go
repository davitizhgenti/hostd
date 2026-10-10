package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitShellAllow(t *testing.T) {
	home := t.TempDir()
	gitDir := filepath.Join(home, "hostd", "git")
	repo := filepath.Join(gitDir, "site.git")
	for _, d := range []string{repo, filepath.Join(home, "other.git"), filepath.Join(gitDir, "site.git", "nested.git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Symlink(filepath.Join(home, "other.git"), filepath.Join(gitDir, "escape.git"))
	for cmd, want := range map[string]string{
		"git-receive-pack '" + repo + "'":                  "receive-pack",
		"git-upload-pack '" + repo + "'":                   "upload-pack",
		"git-receive-pack 'hostd/git/site.git'":            "receive-pack",
		"git-receive-pack '~/hostd/git/site.git'":          "receive-pack",
		"git-receive-pack '" + home + "/other.git'":        "",
		"git-receive-pack 'hostd/git/escape.git'":          "", // a symlink out
		"git-receive-pack 'hostd/git/site.git/nested.git'": "",
		"git-receive-pack 'hostd/git/../../other.git'":     "",
		"git-upload-archive '" + repo + "'":                "",
		"bash -c id":                                       "",
		"git-receive-pack '" + repo + "'; id":              "",
		"git-receive-pack":                                 "",
		"":                                                 "",
	} {
		verb, got, err := gitShellAllow(cmd, home, gitDir)
		if want == "" {
			if err == nil {
				t.Errorf("%q allowed (%s %s)", cmd, verb, got)
			}
			continue
		}
		if err != nil || verb != want || got != repo {
			t.Errorf("%q: %s %s %v", cmd, verb, got, err)
		}
	}
}
