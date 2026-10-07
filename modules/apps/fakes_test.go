package apps

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// fakeSystemd is an in-memory systemd user manager that behaves like the
// real one for what the exec runner uses.
type fakeSystemd struct {
	mu       sync.Mutex
	units    map[string]*fakeUnit
	nextPID  int
	watchers map[int]func(name, sub string)
	nextW    int
	started  []UnitSpec // every spec StartTransient got
}

type fakeUnit struct {
	info UnitInfo
	spec UnitSpec
}

func newFakeSystemd() *fakeSystemd {
	return &fakeSystemd{units: map[string]*fakeUnit{}, nextPID: 1000, watchers: map[int]func(string, string){}}
}

func (f *fakeSystemd) StartTransient(_ context.Context, name string, spec UnitSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.units[name]; ok && u.info.SubState != "dead" {
		return fmt.Errorf("Unit %s was already loaded or has a fragment file", name)
	}
	f.started = append(f.started, spec)
	if filepath.Base(spec.Argv[0]) == "missing" { // Type=exec: the exec itself fails
		f.units[name] = &fakeUnit{info: UnitInfo{Name: name, Description: spec.Description,
			ActiveState: "failed", SubState: "failed", ExitStatus: 203, ExitCode: 1, Result: "exit-code"}, spec: spec}
		return errors.New("start: systemd job failed")
	}
	f.nextPID++
	f.units[name] = &fakeUnit{info: UnitInfo{Name: name, Description: spec.Description,
		ActiveState: "active", SubState: "running", MainPID: f.nextPID}, spec: spec}
	return nil
}

// Stop leaves the unit loaded as inactive/dead, as systemd does until it
// garbage-collects it; that window is where a watcher could mistake a
// stop for an app ending.
func (f *fakeSystemd) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	u, ok := f.units[name]
	if ok && u.info.ActiveState != "failed" {
		u.info.ActiveState, u.info.SubState, u.info.MainPID = "inactive", "dead", 0
	}
	f.mu.Unlock()
	if ok {
		f.notify(name, "dead")
	}
	return nil
}

func (f *fakeSystemd) ResetFailed(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.units[name]; ok && u.info.ActiveState == "failed" {
		delete(f.units, name)
	}
	return nil
}

func (f *fakeSystemd) Unit(_ context.Context, name string) (UnitInfo, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.units[name]
	if !ok {
		return UnitInfo{}, false, nil
	}
	return u.info, true, nil
}

func (f *fakeSystemd) List(_ context.Context, pattern string) ([]UnitInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []UnitInfo
	for name, u := range f.units {
		if u.info.SubState == "dead" {
			continue // inactive units are not listed as loaded
		}
		if ok, _ := filepath.Match(pattern, name); ok {
			out = append(out, u.info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeSystemd) Watch(ctx context.Context, fn func(name, sub string)) error {
	f.mu.Lock()
	f.nextW++
	id := f.nextW
	f.watchers[id] = fn
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	delete(f.watchers, id)
	f.mu.Unlock()
	return nil
}

func (f *fakeSystemd) notify(name, sub string) {
	f.mu.Lock()
	ws := make([]func(string, string), 0, len(f.watchers))
	for _, w := range f.watchers {
		ws = append(ws, w)
	}
	f.mu.Unlock()
	for _, w := range ws {
		w(name, sub)
	}
}

// exit makes a unit's app end the way RemainAfterExit units do: exited
// (status 0) or failed, until someone cleans it up.
func (f *fakeSystemd) exit(name string, status, code int) {
	f.mu.Lock()
	u := f.units[name]
	u.info.ExitStatus, u.info.ExitCode, u.info.MainPID = status, code, 0
	sub := "exited"
	if status != 0 || code != 1 {
		u.info.ActiveState, u.info.SubState, u.info.Result = "failed", "failed", "exit-code"
		sub = "failed"
	} else {
		u.info.SubState = "exited"
	}
	f.mu.Unlock()
	f.notify(name, sub)
}

func (f *fakeSystemd) unitNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, u := range f.units {
		if u.info.SubState == "dead" {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (f *fakeSystemd) watching() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.watchers)
}

// fakeDocker is an in-memory container engine.
type fakeDocker struct {
	mu         sync.Mutex
	images     map[string]bool
	pulled     []string
	containers map[string]*fakeContainer // by ID
	nextID     int
	listeners  map[int]func(id string)
	nextL      int
	created    []ContainerSpec
}

type fakeContainer struct {
	info ContainerInfo
	spec ContainerSpec
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{images: map[string]bool{}, containers: map[string]*fakeContainer{}, listeners: map[int]func(string){}}
}

func (f *fakeDocker) ImageExists(_ context.Context, image string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[image], nil
}

func (f *fakeDocker) Pull(_ context.Context, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if image == "missing:latest" {
		return errors.New("manifest unknown")
	}
	f.pulled = append(f.pulled, image)
	f.images[image] = true
	return nil
}

func (f *fakeDocker) find(idOrName string) *fakeContainer {
	if c, ok := f.containers[idOrName]; ok {
		return c
	}
	for _, c := range f.containers {
		if c.info.Name == idOrName {
			return c
		}
	}
	return nil
}

func (f *fakeDocker) Create(_ context.Context, name string, spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.find(name) != nil {
		return "", fmt.Errorf("name %s in use", name)
	}
	f.nextID++
	id := fmt.Sprintf("c%03d", f.nextID)
	f.created = append(f.created, spec)
	f.containers[id] = &fakeContainer{info: ContainerInfo{ID: id, Name: name, Labels: spec.Labels, Status: "created"}, spec: spec}
	return id, nil
}

func (f *fakeDocker) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return errNotFound
	}
	c.info.Running, c.info.Status, c.info.PID = true, "running", 4000+f.nextID
	return nil
}

func (f *fakeDocker) Stop(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	c := f.find(id)
	if c != nil {
		c.info.Running, c.info.Status, c.info.ExitCode = false, "exited", 143
	}
	f.mu.Unlock()
	if c != nil {
		f.die(c.info.ID)
	}
	return nil
}

func (f *fakeDocker) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.find(id); c != nil {
		delete(f.containers, c.info.ID)
	}
	return nil
}

func (f *fakeDocker) Inspect(_ context.Context, id string) (ContainerInfo, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.find(id); c != nil {
		return c.info, true, nil
	}
	return ContainerInfo{}, false, nil
}

func (f *fakeDocker) List(_ context.Context, label string) ([]ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerInfo
	for _, c := range f.containers {
		if _, ok := c.info.Labels[label]; ok {
			out = append(out, c.info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeDocker) Events(ctx context.Context, _ string, fn func(id string)) error {
	f.mu.Lock()
	f.nextL++
	id := f.nextL
	f.listeners[id] = fn
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	delete(f.listeners, id)
	f.mu.Unlock()
	return nil
}

func (f *fakeDocker) die(id string) {
	f.mu.Lock()
	ls := make([]func(string), 0, len(f.listeners))
	for _, l := range f.listeners {
		ls = append(ls, l)
	}
	f.mu.Unlock()
	for _, l := range ls {
		l(id)
	}
}

// exit makes a container's process end with code; restarting marks it as
// being restarted by the engine.
func (f *fakeDocker) exit(name string, code int, restarting bool) {
	f.mu.Lock()
	c := f.find(name)
	c.info.Running, c.info.Restarting, c.info.ExitCode, c.info.Status = false, restarting, code, "exited"
	id := c.info.ID
	f.mu.Unlock()
	f.die(id)
}

func (f *fakeDocker) watching() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.listeners)
}
