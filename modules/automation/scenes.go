package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

// Scene is a state to reach: a file in the scenes folder, named by its
// ID (scenes/movie.toml is "movie"). Every part is optional.
type Scene struct {
	ID      string        `json:"id" toml:"-"`
	Name    string        `json:"name,omitempty" toml:"name"`
	Audio   *SceneAudio   `json:"audio,omitempty" toml:"audio"`
	Apps    *SceneApps    `json:"apps,omitempty" toml:"apps"`
	Display *SceneDisplay `json:"display,omitempty" toml:"display"`
	// Do are actions of any module, sent last and in file order. Unlike the
	// parts above they are not compared with the current state first: the
	// scene cannot know what an action of another module changes, so each
	// is sent every time the scene is applied.
	Do []SceneDo `json:"do,omitempty" toml:"do"`
}

// SceneDo is one action a scene sends as it stands.
type SceneDo struct {
	Action string         `json:"action" toml:"action"`
	Args   map[string]any `json:"args,omitempty" toml:"args"`
}

// SceneAudio is where sound goes and how loud.
type SceneAudio struct {
	Output string `json:"output,omitempty" toml:"output"` // an output name: hdmi, speakers, bt-...
	Volume *int   `json:"volume,omitempty" toml:"volume"` // 0-150
	Muted  *bool  `json:"muted,omitempty" toml:"muted"`
}

// SceneApps are the apps that run, and the one in front.
type SceneApps struct {
	Stop    []string `json:"stop,omitempty" toml:"stop"`       // stopped (every instance)
	Running []string `json:"running,omitempty" toml:"running"` // started if not running
	Focus   string   `json:"focus,omitempty" toml:"focus"`     // in front
}

// SceneDisplay is whether the screens are on.
type SceneDisplay struct {
	Power string `json:"power,omitempty" toml:"power"` // on, off
}

// Problem is a scene file that cannot be used.
type Problem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

var sceneID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseScene decodes and checks one scene file.
func ParseScene(path string, data []byte) (Scene, error) {
	var s Scene
	md, err := toml.Decode(string(data), &s)
	if err != nil {
		return Scene{}, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return Scene{}, fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}
	s.ID = strings.TrimSuffix(filepath.Base(path), ".toml")
	if !sceneID.MatchString(s.ID) {
		return Scene{}, fmt.Errorf("file name %q: use lowercase letters, digits, '.', '_' and '-'", filepath.Base(path))
	}
	if a := s.Audio; a != nil && a.Volume != nil && (*a.Volume < 0 || *a.Volume > 150) {
		return Scene{}, fmt.Errorf("audio.volume %d: 0-150", *a.Volume)
	}
	if d := s.Display; d != nil && d.Power != "" && d.Power != "on" && d.Power != "off" {
		return Scene{}, fmt.Errorf("display.power %q: on or off", d.Power)
	}
	if a := s.Apps; a != nil {
		stop := map[string]bool{}
		for _, id := range a.Stop {
			stop[id] = true
		}
		for _, id := range a.Running {
			if stop[id] {
				return Scene{}, fmt.Errorf("apps: %q is both in stop and in running", id)
			}
		}
		if stop[a.Focus] {
			return Scene{}, fmt.Errorf("apps.focus %q is in stop", a.Focus)
		}
	}
	for i, d := range s.Do {
		switch {
		case !reActionType.MatchString(d.Action):
			return Scene{}, fmt.Errorf("do %d: action %q: an action type such as lights.scene.set", i+1, d.Action)
		case strings.HasPrefix(d.Action, "scene."):
			return Scene{}, fmt.Errorf("do %d: a scene does not apply scenes (%s); name the other scene's parts here, or use a rule", i+1, d.Action)
		}
	}
	return s, nil
}

// loadScenes reads every scene file.
func loadScenes(dir string) (map[string]Scene, []Problem) {
	scenes := map[string]Scene{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || dir == "" {
		return scenes, nil
	}
	if err != nil {
		return scenes, []Problem{{File: dir, Error: err.Error()}}
	}
	var problems []Problem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err == nil {
			var s Scene
			if s, err = ParseScene(path, b); err == nil {
				scenes[s.ID] = s
				continue
			}
		}
		problems = append(problems, Problem{File: path, Error: err.Error()})
	}
	return scenes, problems
}

// Step is one action a scene needed, and what became of it.
type Step struct {
	Step   string          `json:"step"` // stop, output, volume, mute, start, focus, power, do
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args"`
	// Status: applied, skipped (a higher-priority source holds the
	// resource), failed.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Applied is a scene's result.
type Applied struct {
	Scene string `json:"scene"`
	Steps []Step `json:"steps"` // empty: it was reached already
}

// current is what a scene compares against.
type current struct {
	instances []contract.Instance
	audio     *struct {
		Percent int  `json:"percent"`
		Muted   bool `json:"muted"`
		Outputs []struct {
			Name    string `json:"name"`
			Default bool   `json:"default"`
		} `json:"outputs"`
	}
	display *struct {
		Focused string `json:"focused_instance"`
		Outputs []struct {
			Name   string `json:"name"`
			Active bool   `json:"active"`
			Power  bool   `json:"power"`
		} `json:"outputs"`
	}
}

func (m *Module) read(ctx context.Context, module, name string, into any) error {
	raw, err := m.core.Read(ctx, module, name, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func (c *current) live(app string) []string {
	var ids []string
	for _, in := range c.instances {
		if in.App == app && !in.Ended() {
			ids = append(ids, in.ID)
		}
	}
	return ids
}

// apply reaches the scene: only the actions still needed, in a fixed
// order (stop apps, audio output, volume, start apps, focus, display
// power, then the scene's own do actions). A failed step does not stop the later ones, nor undo the earlier
// ones. Reads of a module that is not there fail that module's steps.
func (m *Module) apply(ctx context.Context, s Scene) Applied {
	out := Applied{Scene: s.ID, Steps: []Step{}}
	var cur current
	appsErr := m.read(ctx, contract.AppsModule, contract.ReadInstances, &cur.instances)
	run := func(step, action string, args any) {
		st := Step{Step: step, Action: action, Args: sdk.MustJSON(args)}
		res, err := m.core.Do(ctx, sdk.Action{Type: action, Args: st.Args})
		switch {
		case err != nil:
			st.Status, st.Error = "failed", err.Error()
		case res.Status == sdk.StatusSkipped:
			st.Status, st.Reason = "skipped", res.Reason
			if res.HeldBy != "" {
				st.Reason = "held by " + string(res.HeldBy)
			}
		default:
			st.Status = "applied"
		}
		out.Steps = append(out.Steps, st)
	}
	failed := func(step string, err error) {
		out.Steps = append(out.Steps, Step{Step: step, Status: "failed", Error: err.Error(), Args: json.RawMessage("null")})
	}

	if a := s.Apps; a != nil && len(a.Stop) > 0 {
		if appsErr != nil {
			failed("stop", appsErr)
		}
		for _, app := range a.Stop {
			for _, id := range cur.live(app) {
				run("stop", contract.ActionInstanceStop, map[string]string{"id": id})
			}
		}
	}

	if a := s.Audio; a != nil {
		if err := m.read(ctx, "audio", "audio", &cur.audio); err != nil {
			failed("audio", err)
		} else {
			if a.Output != "" {
				def := ""
				for _, o := range cur.audio.Outputs {
					if o.Default {
						def = o.Name
					}
				}
				if def != a.Output {
					run("output", "audio.output.set", map[string]string{"output": a.Output})
				}
			}
			if a.Volume != nil && cur.audio.Percent != *a.Volume {
				run("volume", "audio.volume.set", map[string]int{"percent": *a.Volume})
			}
			if a.Muted != nil && cur.audio.Muted != *a.Muted {
				run("mute", "audio.mute.set", map[string]bool{"muted": *a.Muted})
			}
		}
	}

	if a := s.Apps; a != nil && (len(a.Running) > 0 || a.Focus != "") {
		if appsErr != nil {
			failed("start", appsErr)
		} else {
			need := append([]string(nil), a.Running...)
			if a.Focus != "" && !contains(need, a.Focus) {
				need = append(need, a.Focus) // in front means running
			}
			for _, app := range need {
				if len(cur.live(app)) > 0 {
					continue
				}
				run("start", contract.ActionAppStart, map[string]string{"id": app})
			}
		}
	}

	if a := s.Apps; a != nil && a.Focus != "" {
		if err := m.read(ctx, "display", "display", &cur.display); err != nil {
			failed("focus", err)
		} else if focused := cur.display.Focused; appOf(focused) != a.Focus {
			// An app started just now is not in the list: app.start
			// brings it to the front (unless someone uses the screen).
			if live := cur.live(a.Focus); len(live) > 0 {
				run("focus", "window.focus", map[string]string{"instance": live[0]})
			}
		}
	}

	if d := s.Display; d != nil && d.Power != "" {
		if cur.display == nil {
			if err := m.read(ctx, "display", "display", &cur.display); err != nil {
				failed("power", err)
			}
		}
		if cur.display != nil {
			want := d.Power == "on"
			for _, o := range cur.display.Outputs {
				if o.Active && o.Power != want {
					run("power", "display.power", map[string]bool{"on": want})
					break
				}
			}
		}
	}

	for _, d := range s.Do {
		// The module may not be loaded: the core says so, and that step fails.
		args := d.Args
		if args == nil {
			args = map[string]any{}
		}
		run("do", d.Action, args)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// appOf is the app part of an instance ID ("foot#2" → "foot").
func appOf(id string) string {
	app, _, _ := strings.Cut(id, "#")
	return app
}
