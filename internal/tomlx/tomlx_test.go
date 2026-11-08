package tomlx

import (
	"strings"
	"testing"
)

type doc struct {
	Name string `toml:"name"`
	Run  struct {
		Type string `toml:"type"`
	} `toml:"run"`
}

func TestDecodeIsStrict(t *testing.T) {
	var d doc
	if _, err := Decode([]byte("name = \"x\"\n[run]\ntype = \"exec\"\n"), &d); err != nil || d.Run.Type != "exec" {
		t.Fatalf("valid file: %v %+v", err, d)
	}
	_, err := Decode([]byte("zeta = 1\nname = \"x\"\n[run]\ntipe = \"exec\"\n"), &d)
	if err == nil || err.Error() != "unknown key(s): run.tipe, zeta" {
		t.Fatalf("err = %v", err)
	}
	if _, err := Decode([]byte("name = "), &d); err == nil || strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("a syntax error is reported as one: %v", err)
	}
}
