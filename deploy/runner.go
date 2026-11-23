package deploy

import "github.com/davitizhgenti/hostd/internal/execx"

// A Cmd is a command the installer runs; a Runner runs them (ExecRunner on
// the machine, a recording fake in tests). They are hostd's execx types.
type (
	Cmd        = execx.Cmd
	Runner     = execx.Runner
	ExecRunner = execx.OS
)
