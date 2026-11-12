package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated files in the repository match tokens.toml.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	var errb bytes.Buffer
	if code := run([]string{"-check", "-root", "../.."}, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func fakeRepo(t *testing.T, tokens string) string {
	t.Helper()
	root := t.TempDir()
	for p, s := range map[string]string{
		source:                     tokens,
		"deploy/files/sway/config": "a\n" + swayBegin + "\nold\n" + swayEnd + "\nb\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "docs/stylesheets"), 0o755)
	return root
}

func TestWritesEveryTarget(t *testing.T) {
	root := fakeRepo(t, "[color]\nbg = \"#000000\"\ntext = \"#ffffff\"\n[type]\nbody = 27\n[motion]\nbase = 150\n[weight]\nstrong = 600\n")
	var errb bytes.Buffer
	if code := run([]string{"-root", root}, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	css, _ := os.ReadFile(filepath.Join(root, "deploy/files/ui/tokens.css"))
	for _, want := range []string{"--color-bg: #000000;", "--type-body: 27px;", "--motion-base: 150ms;", "--weight-strong: 600;"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("tokens.css lacks %q:\n%s", want, css)
		}
	}
	// In the source's order: bg before text.
	if strings.Index(string(css), "--color-bg") > strings.Index(string(css), "--color-text") {
		t.Error("tokens are out of the source's order")
	}
	docs, _ := os.ReadFile(filepath.Join(root, "docs/stylesheets/tokens.css"))
	if !bytes.Equal(docs, css) {
		t.Error("the docs copy differs")
	}
	var js map[string]map[string]any
	b, _ := os.ReadFile(filepath.Join(root, "deploy/files/ui/tokens.json"))
	if err := json.Unmarshal(b, &js); err != nil || js["color"]["bg"] != "#000000" || js["type"]["body"] != 27.0 || js["units"]["type"] != "px" {
		t.Errorf("tokens.json: %v %s", err, b)
	}
	sway, _ := os.ReadFile(filepath.Join(root, "deploy/files/sway/config"))
	if want := "a\n" + swayBegin + "\noutput * bg #000000 solid_color\n" + swayEnd + "\nb\n"; string(sway) != want {
		t.Errorf("sway config:\n%s", sway)
	}
	// Now current: -check passes, and a second run writes nothing.
	errb.Reset()
	if code := run([]string{"-check", "-root", root}, &errb); code != 0 {
		t.Fatalf("check after writing: %d %s", code, errb.String())
	}
	if code := run([]string{"-root", root}, &errb); code != 0 || strings.Contains(errb.String(), "wrote") {
		t.Errorf("second run: %d %s", code, errb.String())
	}
}

func TestCheckFindsStaleFiles(t *testing.T) {
	root := fakeRepo(t, "[color]\nbg = \"#000000\"\n")
	var errb bytes.Buffer
	if code := run([]string{"-check", "-root", root}, &errb); code != 1 || !strings.Contains(errb.String(), "tokens.css is out of date") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(root, "deploy/files/ui/tokens.css")); err == nil {
		t.Error("-check wrote a file")
	}
}

func TestBadSources(t *testing.T) {
	for name, tokens := range map[string]string{
		"not toml":  "[color\n",
		"a table":   "[color]\nbg = { a = 1 }\n",
		"no tokens": "",
		"no bg":     "[color]\ntext = \"#fff\"\n",
		"a bool":    "[color]\nbg = true\n",
	} {
		root := fakeRepo(t, tokens)
		var errb bytes.Buffer
		if code := run([]string{"-root", root}, &errb); code != 1 {
			t.Errorf("%s: exit %d: %s", name, code, errb.String())
		}
	}
	root := fakeRepo(t, "[color]\nbg = \"#000\"\n")
	os.WriteFile(filepath.Join(root, "deploy/files/sway/config"), []byte("no markers\n"), 0o644)
	var errb bytes.Buffer
	if code := run([]string{"-root", root}, &errb); code != 1 || !strings.Contains(errb.String(), "markers") {
		t.Errorf("no markers: %d %s", code, errb.String())
	}
	if code := run([]string{"-nope"}, &errb); code != 2 {
		t.Errorf("bad flag: %d", code)
	}
}
