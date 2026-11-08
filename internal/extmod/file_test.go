package extmod

import (
	"strings"
	"testing"
)

func TestParseFile(t *testing.T) {
	f, err := ParseFile("lights", []byte("command = [\"python3\", \"lights.py\"]\nenv = { A = \"1\" }\ntransport = \"stdio\"\n"), []string{"apps"})
	if err != nil || len(f.Command) != 2 || f.Env["A"] != "1" || f.Transport != TransportStdio {
		t.Fatalf("%+v %v", f, err)
	}
	for _, c := range []struct{ name, body, want string }{
		{"lights", "env = {}", "needs a command"},
		{"lights", "command = [\"\"]", "needs a command"},
		{"lights", "command = [\"x\"]\ntransport = \"pipe\"", `transport "pipe"`},
		{"lights", "command = [\"x\"]\ncommnd = 1", "unknown key(s): commnd"},
		{"Lights", "command = [\"x\"]", "use lowercase"},
		{"my_lights", "command = [\"x\"]", "use lowercase"},
		{"apps", "command = [\"x\"]", "built-in module"},
	} {
		if _, err := ParseFile(c.name, []byte(c.body), []string{"apps"}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: %v, want %q", c.name, c.body, err, c.want)
		}
	}
	if _, err := ParseFile("my-lights", []byte("command = [\"x\"]"), nil); err != nil {
		t.Errorf("a dash is fine: %v", err)
	}
}
