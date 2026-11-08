package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/davitizhgenti/hostd/internal/extmod"
	"github.com/davitizhgenti/hostd/internal/system"
)

// An external module is a file in modules/ beside hostd.toml: the file's name
// is the module's, and its content says how to start it (extmod.File). hostd
// starts them when it starts; the kind "module" shows what became of each.

// external is one file of modules/.
type external struct {
	name string
	raw  []byte
	file extmod.File
	err  error
}

// loadExternals reads modules/*.toml, by name. A file that is not valid is
// kept with its error, so it shows on the resource; hostd runs without it.
func loadExternals(configDir string) []external {
	dir := filepath.Join(configDir, "modules")
	entries, _ := os.ReadDir(dir)
	var out []external
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".toml") {
			continue
		}
		x := external{name: strings.TrimSuffix(n, ".toml")}
		if x.raw, x.err = os.ReadFile(filepath.Join(dir, n)); x.err == nil {
			x.file, x.err = extmod.ParseFile(x.name, x.raw, builtinNames())
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// state is what the hostd module is told of an external module.
func (x external) state(runErr error) system.ExternalState {
	st := system.ExternalState{Name: x.name, Raw: x.raw}
	switch {
	case x.err != nil:
		st.Err = x.err.Error()
	case runErr != nil:
		st.Err = fmt.Sprint(runErr)
	}
	return st
}
