package apps

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// AppFile is a hand-written app definition, ~/.config/hostd/apps/*.toml.
// Pointer fields tell "not set" apart from a zero value, so a file that
// extends a discovered app changes only what it sets.
type AppFile struct {
	Path string `toml:"-"`

	ID       string            `toml:"id"`
	Extends  string            `toml:"extends"`
	Name     *string           `toml:"name"`
	Icon     *string           `toml:"icon"`
	Runner   *Runner           `toml:"runner"`
	Surface  *string           `toml:"surface"`
	Window   *fileWindow       `toml:"window"`
	Instance *fileInstance     `toml:"instance"`
	Audio    *fileAudio        `toml:"audio"`
	Requires []string          `toml:"requires"`
	Hidden   *bool             `toml:"hidden"`
	Match    *Match            `toml:"match"`
	Restart  *string           `toml:"restart"`
	Env      map[string]string `toml:"env"`
	Health   *Health           `toml:"health"`
	// Actions add to the app's actions; one with the ID of an existing one
	// replaces it.
	Actions []AppAction `toml:"actions"`
	// Source is how a background app's new versions arrive (deploy
	// module, M4); accepted now so files can already declare it.
	Source any `toml:"source"`
}

type fileWindow struct {
	Fullscreen *bool   `toml:"fullscreen"`
	Wrap       *string `toml:"wrap"`
}

type fileInstance struct {
	Policy    *string `toml:"policy"`
	IfRunning *string `toml:"if_running"`
}

type fileAudio struct {
	Volume *int `toml:"volume"`
}

// applyTo overlays the fields the file sets onto a.
func (f AppFile) applyTo(a *App) {
	if f.Name != nil {
		a.Name = *f.Name
	}
	if f.Icon != nil {
		a.Icon = *f.Icon
	}
	if f.Runner != nil {
		a.Runner = *f.Runner
		a.Surface = "" // re-derive from the new runner unless the file sets it
	}
	if f.Surface != nil {
		a.Surface = *f.Surface
	}
	if w := f.Window; w != nil {
		if w.Fullscreen != nil {
			a.Window.Fullscreen = *w.Fullscreen
		}
		if w.Wrap != nil {
			a.Window.Wrap = *w.Wrap
		}
	}
	if in := f.Instance; in != nil {
		if in.Policy != nil {
			a.Instance.Policy = *in.Policy
		}
		if in.IfRunning != nil {
			a.Instance.IfRunning = *in.IfRunning
		}
	}
	if f.Audio != nil && f.Audio.Volume != nil {
		v := *f.Audio.Volume
		a.Audio.Volume = &v
	}
	if f.Requires != nil {
		a.Requires = f.Requires
	}
	if f.Hidden != nil {
		a.Hidden = *f.Hidden
	}
	if f.Match != nil {
		a.Match = *f.Match
	}
	if f.Restart != nil {
		a.Restart = *f.Restart
	}
	if f.Env != nil {
		a.Env = f.Env
	}
	if f.Health != nil {
		a.Health = *f.Health
	}
	if len(f.Actions) > 0 {
		merged := append([]AppAction(nil), a.Actions...)
		for _, x := range f.Actions {
			if i := slices.IndexFunc(merged, func(y AppAction) bool { return y.ID == x.ID }); i >= 0 {
				merged[i] = x
			} else {
				merged = append(merged, x)
			}
		}
		a.Actions = merged
	}
}

// ParseAppFile decodes one app file. Unknown keys are errors, so a typo
// such as "fulscreen" is reported instead of silently ignored.
func ParseAppFile(path string, data []byte) (AppFile, error) {
	var f AppFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return AppFile{}, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return AppFile{}, fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}
	f.Path = path
	if f.ID == "" && f.Extends == "" {
		// The file name is the ID: apps/jellyfin.toml defines "jellyfin".
		f.ID = strings.TrimSuffix(filepath.Base(path), ".toml")
	}
	return f, nil
}

// LoadAppFiles reads every *.toml in dir. A missing dir is not an error.
func LoadAppFiles(dir string) ([]AppFile, []Problem) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []Problem{{File: dir, Error: err.Error()}}
	}
	var files []AppFile
	var problems []Problem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, Problem{File: path, Error: err.Error()})
			continue
		}
		f, err := ParseAppFile(path, data)
		if err != nil {
			problems = append(problems, Problem{File: path, Error: err.Error()})
			continue
		}
		files = append(files, f)
	}
	return files, problems
}
