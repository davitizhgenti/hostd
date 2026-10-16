package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Two test binaries' counts for the same blocks: covered if either ran it.
func TestReadProfile(t *testing.T) {
	p := write(t, `mode: set
github.com/davitizhgenti/hostd/core/engine.go:10.1,12.2 3 0
github.com/davitizhgenti/hostd/core/engine.go:14.1,15.2 1 1
github.com/davitizhgenti/hostd/core/engine.go:10.1,12.2 3 1
github.com/davitizhgenti/hostd/cmd/hostd/main.go:5.1,6.2 2 0
`)
	cov, err := readProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c := cov["core"]; c.covered != 4 || c.total != 4 || c.pct() != 100 {
		t.Fatalf("core %+v", c)
	}
	if c := cov["cmd/hostd"]; c.covered != 0 || c.total != 2 {
		t.Fatalf("cmd/hostd %+v", c)
	}
	if (counts{}).pct() != 100 {
		t.Fatal("a package with no statements")
	}
	if _, err := readProfile(write(t, "mode: set\nnot a profile line\n")); err == nil {
		t.Fatal("read a broken profile")
	}
	if _, err := readProfile("/no/such/file"); err == nil {
		t.Fatal("read a missing profile")
	}
}

func TestFloors(t *testing.T) {
	floors, err := readFloors(write(t, "# a comment\n*  80\ncore/...  85\nmodules/apps 90  # its own\n\ncmd/hostd 0%\n"))
	if err != nil {
		t.Fatal(err)
	}
	for pkg, want := range map[string]float64{"sdk": 80, "core": 85, "core/api": 85, "corefake": 80, "modules/apps": 90,
		"modules/apps/catalog": 80, "cmd/hostd": 0} {
		if got := floorFor(pkg, floors).min; got != want {
			t.Errorf("%s: %v, want %v", pkg, got, want)
		}
	}
	if _, err := readFloors(write(t, "core\n")); err == nil {
		t.Fatal("a floor without a percent")
	}
	if _, err := readFloors("/no/such/file"); err == nil {
		t.Fatal("read missing floors")
	}
}

func TestRun(t *testing.T) {
	profile := write(t, "mode: set\ngithub.com/davitizhgenti/hostd/core/a.go:1.1,2.2 1 1\ngithub.com/davitizhgenti/hostd/sdk/b.go:1.1,2.2 1 0\n")
	floors := write(t, "* 80\n")
	var out, errs bytes.Buffer
	if code := run([]string{profile, floors}, &out, &errs); code != 1 || !strings.Contains(out.String(), "✗   0.0%  (floor 80%)  sdk") ||
		!strings.Contains(out.String(), "1 package(s) below") {
		t.Fatalf("%d\n%s", code, out.String())
	}
	out.Reset()
	if code := run([]string{profile, write(t, "* 0\n")}, &out, &errs); code != 0 || !strings.Contains(out.String(), "every package") {
		t.Fatalf("%d\n%s", code, out.String())
	}
	for _, args := range [][]string{{}, {"/none", floors}, {profile, "/none"}} {
		if code := run(args, &out, &errs); code != 2 {
			t.Errorf("%q: %d", args, code)
		}
	}
}
