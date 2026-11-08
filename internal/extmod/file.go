package extmod

import (
	"errors"
	"fmt"
	"strings"

	"github.com/davitizhgenti/hostd/internal/tomlx"
	"github.com/davitizhgenti/hostd/sdk"
)

// File is an external module as its file says it: modules/<name>.toml, the
// resource of the kind "module". The file's name is the module's, and must
// be the name its manifest gives.
//
//	command   = ["python3", "/home/screen/hostd/modules/lights.py"]
//	env       = { LIGHTS_BRIDGE = "192.168.1.20" }
//	transport = "stdio"   # optional: talk over the program's stdin/stdout, not a socket
type File struct {
	Command   []string          `toml:"command" json:"command" desc:"the program and its arguments"`
	Env       map[string]string `toml:"env" json:"env,omitempty" desc:"added to the program's environment"`
	Transport string            `toml:"transport" json:"transport,omitempty" enum:"socket,stdio" desc:"socket (default) or stdio"`
}

// ParseFile decodes and checks a module file. reserved are names that
// belong to built-in modules.
func ParseFile(name string, raw []byte, reserved []string) (File, error) {
	var f File
	if _, err := tomlx.Decode(raw, &f); err != nil {
		return f, err
	}
	switch {
	case !sdk.ValidModuleName(name):
		return f, fmt.Errorf("the name %q: use lowercase letters, digits and - (up to 32)", name)
	case contains(reserved, name):
		return f, fmt.Errorf("%q is the name of a built-in module", name)
	case len(f.Command) == 0 || strings.TrimSpace(f.Command[0]) == "":
		return f, errors.New("needs a command")
	case f.Transport != "" && f.Transport != TransportSocket && f.Transport != TransportStdio:
		return f, fmt.Errorf("transport %q: use %q or %q", f.Transport, TransportSocket, TransportStdio)
	}
	return f, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
