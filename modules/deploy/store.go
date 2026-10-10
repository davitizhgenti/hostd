package deploy

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Release is one deployed version of a service.
type Release struct {
	Rev      string    `json:"rev"` // the commit
	Dir      string    `json:"dir"`
	Instance string    `json:"instance,omitempty"`
	Port     int       `json:"port,omitempty"` // internal: the proxy's upstream
	At       time.Time `json:"at"`
	// Result: live (serving), stopped (replaced), failed, rolled back.
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// State is a service's releases, newest first, and which one serves.
type State struct {
	App      string    `json:"app"`
	Current  string    `json:"current,omitempty"` // Rev of the live release
	Releases []Release `json:"releases"`
}

// current returns the live release.
func (s *State) current() (Release, bool) {
	for _, r := range s.Releases {
		if r.Rev == s.Current && r.Result == "live" {
			return r, true
		}
	}
	return Release{}, false
}

// record puts a release first (replacing an older entry for the same rev).
func (s *State) record(r Release) {
	out := []Release{r}
	for _, o := range s.Releases {
		if o.Rev != r.Rev || o.At != r.At {
			out = append(out, o)
		}
	}
	s.Releases = out
}

// set changes a release's entry.
func (s *State) set(rev string, at time.Time, fn func(*Release)) {
	for i := range s.Releases {
		if s.Releases[i].Rev == rev && s.Releases[i].At.Equal(at) {
			fn(&s.Releases[i])
			return
		}
	}
}

func (m *Module) statePath(app string) string {
	return filepath.Join(m.opts.Root, "releases", app, "state.json")
}

func (m *Module) load(app string) (*State, error) {
	b, err := os.ReadFile(m.statePath(app))
	if errors.Is(err, fs.ErrNotExist) {
		return &State{App: app}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	s.App = app
	return &s, nil
}

func (m *Module) save(s *State) error {
	path := m.statePath(s.App)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// prune removes release directories beyond the newest keep (never the
// live one), and their history.
func (m *Module) prune(s *State, keep int) {
	if keep <= 0 {
		keep = 5
	}
	sort.SliceStable(s.Releases, func(i, j int) bool { return s.Releases[i].At.After(s.Releases[j].At) })
	var kept []Release
	dirs := map[string]bool{}
	for _, r := range s.Releases {
		if len(kept) < keep || r.Rev == s.Current {
			kept = append(kept, r)
			dirs[r.Dir] = true
		}
	}
	for _, r := range s.Releases {
		if !dirs[r.Dir] && r.Dir != "" {
			_ = os.RemoveAll(r.Dir)
		}
	}
	s.Releases = kept
}
