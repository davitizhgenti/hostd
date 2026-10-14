// Package unitstest has a fake systemd user manager for tests of the exec
// runner and of the apps module that runs it.
package unitstest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/davitizhgenti/hostd/internal/systemd"
)

// FakeSystemd is an in-memory systemd user manager that behaves like the
// real one for what the exec runner uses. Tests read and change its units
// directly, holding Mu.
type FakeSystemd struct {
	Mu       sync.Mutex
	Units    map[string]*FakeUnit
	nextPID  int
	watchers map[int]func(name, sub string)
	nextW    int
	Started  []systemd.UnitSpec // every spec StartTransient got
	broken   chan struct{}      // closed by BreakWatch
}

type FakeUnit struct {
	Info systemd.UnitInfo
	Spec systemd.UnitSpec
}

func NewFakeSystemd() *FakeSystemd {
	return &FakeSystemd{Units: map[string]*FakeUnit{}, nextPID: 1000, watchers: map[int]func(string, string){}}
}

func (f *FakeSystemd) StartTransient(_ context.Context, name string, spec systemd.UnitSpec) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if u, ok := f.Units[name]; ok && u.Info.SubState != "dead" {
		return fmt.Errorf("Unit %s was already loaded or has a fragment file", name)
	}
	f.Started = append(f.Started, spec)
	if filepath.Base(spec.Argv[0]) == "missing" { // Type=exec: the exec itself fails
		f.Units[name] = &FakeUnit{Info: systemd.UnitInfo{Name: name, Description: spec.Description,
			ActiveState: "failed", SubState: "failed", ExitStatus: 203, ExitCode: 1, Result: "exit-code"}, Spec: spec}
		return errors.New("start: systemd job failed")
	}
	f.nextPID++
	f.Units[name] = &FakeUnit{Info: systemd.UnitInfo{Name: name, Description: spec.Description,
		ActiveState: "active", SubState: "running", MainPID: f.nextPID}, Spec: spec}
	return nil
}

// Stop leaves the unit loaded as inactive/dead, as systemd does until it
// garbage-collects it; that window is where a watcher could mistake a
// stop for an app ending.
func (f *FakeSystemd) Stop(_ context.Context, name string) error {
	f.Mu.Lock()
	u, ok := f.Units[name]
	if ok && u.Info.ActiveState != "failed" {
		u.Info.ActiveState, u.Info.SubState, u.Info.MainPID = "inactive", "dead", 0
	}
	f.Mu.Unlock()
	if ok {
		f.Notify(name, "dead")
	}
	return nil
}

func (f *FakeSystemd) ResetFailed(_ context.Context, name string) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if u, ok := f.Units[name]; ok && u.Info.ActiveState == "failed" {
		delete(f.Units, name)
	}
	return nil
}

func (f *FakeSystemd) Unit(_ context.Context, name string) (systemd.UnitInfo, bool, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	u, ok := f.Units[name]
	if !ok {
		return systemd.UnitInfo{}, false, nil
	}
	return u.Info, true, nil
}

func (f *FakeSystemd) List(_ context.Context, pattern string) ([]systemd.UnitInfo, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	var out []systemd.UnitInfo
	for name, u := range f.Units {
		if u.Info.SubState == "dead" {
			continue // inactive units are not listed as loaded
		}
		if ok, _ := filepath.Match(pattern, name); ok {
			out = append(out, u.Info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *FakeSystemd) Watch(ctx context.Context, fn func(name, sub string)) error {
	f.Mu.Lock()
	f.nextW++
	id := f.nextW
	f.watchers[id] = fn
	if f.broken == nil {
		f.broken = make(chan struct{})
	}
	broken := f.broken
	f.Mu.Unlock()
	defer func() {
		f.Mu.Lock()
		delete(f.watchers, id)
		f.Mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil
	case <-broken:
		return errors.New("the bus connection closed")
	}
}

// BreakWatch ends every Watch with an error, as a lost D-Bus connection
// does.
func (f *FakeSystemd) BreakWatch() {
	f.Mu.Lock()
	if f.broken == nil {
		f.broken = make(chan struct{})
	}
	close(f.broken)
	f.broken = nil
	f.Mu.Unlock()
}

// ExitQuietly ends a unit's app without telling any watcher (the event is
// lost, as while the bus connection is down).
func (f *FakeSystemd) ExitQuietly(name string, status int) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	u := f.Units[name]
	u.Info.ExitStatus, u.Info.ExitCode, u.Info.MainPID = status, 1, 0
	if status != 0 {
		u.Info.ActiveState, u.Info.SubState, u.Info.Result = "failed", "failed", "exit-code"
	} else {
		u.Info.SubState = "exited"
	}
}

func (f *FakeSystemd) Notify(name, sub string) {
	f.Mu.Lock()
	ws := make([]func(string, string), 0, len(f.watchers))
	for _, w := range f.watchers {
		ws = append(ws, w)
	}
	f.Mu.Unlock()
	for _, w := range ws {
		w(name, sub)
	}
}

// Exit makes a unit's app end the way RemainAfterExit units do: exited
// (status 0) or failed, until someone cleans it up.
func (f *FakeSystemd) Exit(name string, status, code int) {
	f.Mu.Lock()
	u := f.Units[name]
	u.Info.ExitStatus, u.Info.ExitCode, u.Info.MainPID = status, code, 0
	sub := "exited"
	if status != 0 || code != 1 {
		u.Info.ActiveState, u.Info.SubState, u.Info.Result = "failed", "failed", "exit-code"
		sub = "failed"
	} else {
		u.Info.SubState = "exited"
	}
	f.Mu.Unlock()
	f.Notify(name, sub)
}

func (f *FakeSystemd) UnitNames() []string {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	var out []string
	for n, u := range f.Units {
		if u.Info.SubState == "dead" {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (f *FakeSystemd) Watching() int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return len(f.watchers)
}
