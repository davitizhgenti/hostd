package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/sdk"
)

func lightsManifest() sdk.Manifest {
	return sdk.Manifest{Name: "lights", Version: "1", Owns: []string{"lights.*"},
		Reads: []sdk.ReadSpec{
			{Name: "lights", Description: "Every light", Path: "/v1/lights", CLI: &sdk.CLI{
				Alias: []string{"lamps", "login"}, // login is a built-in: not taken
				Empty: "no lights",
				Columns: []sdk.Column{
					{Title: "LIGHT", Path: "id"}, {Title: "LEVEL", Path: "brightness"}, {Title: "ROOM", Path: "where.room|zone", Max: 8},
					{Title: "TAGS", Path: "tags", Format: "list"}, {Title: "LAST", Path: "changed", Format: "time"}}}},
			{Name: "light", Description: "One light", Path: "/v1/lights/{id}"},
			{Name: "history", Path: "/v1/lights/{id}/history/{day}"},
		}}
}

func TestGetPrintsATable(t *testing.T) {
	fakeAPI(t, []sdk.Manifest{lightsManifest()}, map[string]string{
		"/v1/lights": `[
			{"id":"porch","brightness":40,"where":{"room":"outside"},"tags":["a","b"],"changed":"2026-10-14T10:15:30Z"},
			{"id":"living-room","brightness":0,"zone":"a room with a very long name","tags":[],"changed":"not a time"},
			{"id":"hall","brightness":null,"where":{"room":""}}]`,
	})

	out, errs, code := hostctl(t, "get", "lights", "lights")
	want := `LIGHT        LEVEL  ROOM      TAGS  LAST
porch        40     outside   a, b  10:15:30
living-room  0      a room …  -     not a time
hall         -      -         -     -
`
	if code != 0 || out != want {
		t.Fatalf("exit %d %s\n%s\nwant\n%s", code, errs, out, want)
	}

	// A read named as its module is also "hostctl get <module>".
	if out2, _, code := hostctl(t, "get", "lights"); code != 0 || out2 != want {
		t.Fatalf("get lights: exit %d\n%s", code, out2)
	}
	if _, _, code := hostctl(t, "get", "lights", "lamp"); code != 2 {
		t.Fatalf("get lights lamp: exit %d, want a usage error", code)
	}

	// The alias is the same command; one that a built-in has is not used.
	if out2, _, code := hostctl(t, "lamps"); code != 0 || out2 != want {
		t.Fatalf("alias: exit %d\n%s", code, out2)
	}
	if out3, _, _ := hostctl(t, "login", "--help"); strings.Contains(out3, "LIGHT") || strings.Contains(out3, "Every light") {
		t.Fatalf("an alias took a built-in's name:\n%s", out3)
	}

	// --json is the server's JSON, not a table.
	out, _, code = hostctl(t, "get", "lights", "lights", "--json")
	if code != 0 || !strings.Contains(out, `"brightness": 40`) || strings.Contains(out, "LIGHT") {
		t.Fatalf("--json: exit %d\n%s", code, out)
	}
}

func TestGetEmptyAndOddAnswers(t *testing.T) {
	fakeAPI(t, []sdk.Manifest{lightsManifest()}, map[string]string{"/v1/lights": `[]`})
	if out, _, code := hostctl(t, "get", "lights", "lights"); code != 0 || out != "no lights\n" {
		t.Fatalf("empty: %q (exit %d)", out, code)
	}
	// A read that does not return a list of objects is shown as JSON.
	fakeAPI(t, []sdk.Manifest{lightsManifest()}, map[string]string{"/v1/lights": `{"error":"none"}`})
	if out, _, code := hostctl(t, "get", "lights", "lights"); code != 0 || !strings.Contains(out, `"error": "none"`) {
		t.Fatalf("an object: %q (exit %d)", out, code)
	}
}

func TestGetPathAndQueryParameters(t *testing.T) {
	fakeAPI(t, []sdk.Manifest{lightsManifest()}, map[string]string{
		"/v1/lights/a%20b": `{"id":"a b"}`,
		"/v1/lights/porch/history/monday?n=5&x=a%26b": `{"day":"monday"}`,
	})
	if out, errs, code := hostctl(t, "get", "lights", "light", "a b"); code != 0 || !strings.Contains(out, `"id": "a b"`) {
		t.Fatalf("path parameter: %q %s (exit %d)", out, errs, code)
	}
	if out, errs, code := hostctl(t, "get", "lights", "history", "porch", "monday", "--param", "n=5", "--param", "x=a&b"); code != 0 || !strings.Contains(out, `"day": "monday"`) {
		t.Fatalf("two parameters and a query: %q %s (exit %d)", out, errs, code)
	}
	// Wrong use is a usage error, not a request.
	for _, args := range [][]string{{"get", "lights", "light"}, {"get", "lights", "light", "a", "b"}, {"get", "lights", "lights", "--param", "novalue"}} {
		if _, _, code := hostctl(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	// What the server says goes through as the usual exit code.
	if _, errs, code := hostctl(t, "get", "lights", "light", "ghost"); code != exitNotFound || !strings.Contains(errs, "ghost") {
		t.Errorf("a missing light: exit %d, %s", code, errs)
	}
}

func TestGetListsWhatIsThere(t *testing.T) {
	fakeAPI(t, []sdk.Manifest{lightsManifest()}, nil)
	out, _, code := hostctl(t, "get", "--help")
	if code != 0 || !strings.Contains(out, "lights") {
		t.Fatalf("get --help: exit %d\n%s", code, out)
	}
	out, _, _ = hostctl(t, "get", "lights", "--help")
	for _, want := range []string{"history", "light ", "lights ", "Every light", "One light"} {
		if !strings.Contains(out, want) {
			t.Errorf("get lights --help lacks %q:\n%s", want, out)
		}
	}
	out, _, _ = hostctl(t, "get", "lights", "history", "--help")
	if !strings.Contains(out, "hostctl get lights history <id> <day> [flags]") || !strings.Contains(out, "--param") {
		t.Errorf("history --help:\n%s", out)
	}
	// Without a module that has reads there is no get.
	fakeAPI(t, []sdk.Manifest{{Name: "bare", Version: "1", Owns: []string{"bare.*"}}}, nil)
	if _, _, code := hostctl(t, "get"); code == 0 {
		t.Error("get exists with nothing to get")
	}
}

func TestAliasForAnAction(t *testing.T) {
	m := sdk.Manifest{Name: "lights", Version: "1", Owns: []string{"lights.*"},
		Actions: []sdk.ActionSpec{{Type: "lights.set", Scope: "lights", CLI: &sdk.CLI{Alias: []string{"dim"}},
			Schema: []byte(`{"type":"object","properties":{"id":{"type":"string"},"brightness":{"type":"integer"}},"required":["id","brightness"]}`)}}}
	fakeAPI(t, []sdk.Manifest{m}, nil)
	out, _, code := hostctl(t, "dim", "--help")
	if code != 0 || !strings.Contains(out, "dim <id> <brightness>") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// A read that wraps its list in an object names the field (rows); a null
// answer is an empty list; a field that is not a list falls back to JSON.
func TestGetRowsInAnObject(t *testing.T) {
	m := sdk.Manifest{Name: "auto", Version: "1", Owns: []string{"auto.*"},
		Reads: []sdk.ReadSpec{
			{Name: "rules", Path: "/v1/rules", CLI: &sdk.CLI{Rows: "rules", Empty: "no rules",
				Columns: []sdk.Column{{Title: "RULE", Path: "name"}, {Title: "DO", Path: "do.scene|do.action"}}}},
			{Name: "media", Path: "/v1/media", CLI: &sdk.CLI{Empty: "no media players", Columns: []sdk.Column{{Title: "PLAYER", Path: "name"}}}},
			{Name: "odd", Path: "/v1/odd", CLI: &sdk.CLI{Rows: "rules", Columns: []sdk.Column{{Title: "A", Path: "a"}}}},
		}}
	fakeAPI(t, []sdk.Manifest{m}, map[string]string{
		"/v1/rules": `{"rules": [{"name": "evening", "do": {"scene": "dim"}}, {"name": "quiet", "do": {"action": "audio.mute.set"}}], "problems": []}`,
		"/v1/media": `null`,
		"/v1/odd":   `{"rules": "not a list"}`,
	})
	out, _, code := hostctl(t, "get", "auto", "rules")
	if want := "RULE     DO\nevening  dim\nquiet    audio.mute.set\n"; code != 0 || out != want {
		t.Fatalf("rules: %q (exit %d)", out, code)
	}
	if out, _, code := hostctl(t, "get", "auto", "media"); code != 0 || out != "no media players\n" {
		t.Fatalf("null: %q (exit %d)", out, code)
	}
	if out, _, code := hostctl(t, "get", "auto", "odd"); code != 0 || !strings.Contains(out, `"rules": "not a list"`) {
		t.Fatalf("not a list: %q (exit %d)", out, code)
	}
}

func TestGroupsListTheirCommands(t *testing.T) {
	m := lightsManifest()
	m.Actions = []sdk.ActionSpec{{Type: "lights.scene.set", Scope: "lights"}, {Type: "lights.scene.clear", Scope: "lights"}}
	fakeAPI(t, []sdk.Manifest{m}, nil)
	out, errs, code := hostctl(t, "lights", "--help")
	if code != 0 || !regexp.MustCompile(`scene +set, clear|scene +clear, set`).MatchString(out) {
		t.Fatalf("exit %d %s\n%s", code, errs, out)
	}
}
