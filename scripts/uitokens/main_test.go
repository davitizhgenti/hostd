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
	os.MkdirAll(filepath.Join(root, "deploy/files/shell/qml/Hostd"), 0o755)
	return root
}

func TestWritesEveryTarget(t *testing.T) {
	root := fakeRepo(t, "[color]\nbg = \"#000000\"\ntext = \"#ffffff\"\n[font]\ntext = \"Inter\"\n[type]\nbody = 27\n"+
		"[motion]\nbase = 150\n[weight]\nstrong = 600\n[layout]\nsafe-margin = 48\n")
	var errb bytes.Buffer
	if code := run([]string{"-root", root}, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	css, _ := os.ReadFile(filepath.Join(root, "docs/stylesheets/tokens.css"))
	for _, want := range []string{"--color-bg: #000000;", "--type-body: 27px;", "--motion-base: 150ms;", "--weight-strong: 600;",
		`--font-text: "Inter", sans-serif;`, "--layout-safe-margin: 48px;"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("tokens.css lacks %q:\n%s", want, css)
		}
	}
	// In the source's order: bg before text.
	if strings.Index(string(css), "--color-bg") > strings.Index(string(css), "--color-text") {
		t.Error("tokens are out of the source's order")
	}
	qml, _ := os.ReadFile(filepath.Join(root, "deploy/files/shell/qml/Hostd/Theme.qml"))
	for _, want := range []string{"pragma Singleton", "property real scale: 1.0",
		`readonly property color colorBg: "#000000"`,
		`readonly property string fontText: "Inter"`, "readonly property int typeBody: Math.max(1, Math.round(27 * scale))",
		"property real animate: 1.0", "readonly property int motionBase: Math.round(150 * animate)",
		"readonly property int layoutSafeMargin: Math.max(1, Math.round(48 * scale))"} {
		if !strings.Contains(string(qml), want) {
			t.Errorf("Theme.qml lacks %q:\n%s", want, qml)
		}
	}
	var js map[string]map[string]any
	b, _ := os.ReadFile(filepath.Join(root, "deploy/files/shell/tokens.json"))
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
	if code := run([]string{"-check", "-root", root}, &errb); code != 1 || !strings.Contains(errb.String(), "Theme.qml is out of date") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(root, "deploy/files/shell/qml/Hostd/Theme.qml")); err == nil {
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
