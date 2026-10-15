// Package podmantest has a fake container engine (the Podman API) for tests
// of the container runners and of the apps module that runs them.
package podmantest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/modules/apps/runner/podman"
)

// FakeEngine is an in-memory container engine.
type FakeEngine struct {
	Mu         sync.Mutex
	Images     map[string]bool
	Pulled     []string
	Containers map[string]*FakeContainer // by ID
	nextID     int
	listeners  map[int]func(id string)
	nextL      int
	Created    []podman.ContainerSpec
}

type FakeContainer struct {
	Info podman.ContainerInfo
	Spec podman.ContainerSpec
}

func NewFakeEngine() *FakeEngine {
	return &FakeEngine{Images: map[string]bool{}, Containers: map[string]*FakeContainer{}, listeners: map[int]func(string){}}
}

func (f *FakeEngine) ImageExists(_ context.Context, image string) (bool, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.Images[image], nil
}

func (f *FakeEngine) Pull(_ context.Context, image string) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if image == "missing:latest" {
		return errors.New("manifest unknown")
	}
	f.Pulled = append(f.Pulled, image)
	f.Images[image] = true
	return nil
}

func (f *FakeEngine) Find(idOrName string) *FakeContainer {
	if c, ok := f.Containers[idOrName]; ok {
		return c
	}
	for _, c := range f.Containers {
		if c.Info.Name == idOrName {
			return c
		}
	}
	return nil
}

func (f *FakeEngine) Create(_ context.Context, name string, spec podman.ContainerSpec) (string, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.Find(name) != nil {
		return "", fmt.Errorf("name %s in use", name)
	}
	f.nextID++
	id := fmt.Sprintf("c%03d", f.nextID)
	f.Created = append(f.Created, spec)
	f.Containers[id] = &FakeContainer{Info: podman.ContainerInfo{ID: id, Name: name, Labels: spec.Labels, Status: "created"}, Spec: spec}
	return id, nil
}

func (f *FakeEngine) Start(_ context.Context, id string) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	c := f.Find(id)
	if c == nil {
		return podman.ErrNotFound
	}
	c.Info.Running, c.Info.Status, c.Info.PID = true, "running", 4000+f.nextID
	c.Info.Started = time.Date(2020, 1, 1, 0, 0, f.nextID, 0, time.UTC)
	return nil
}

func (f *FakeEngine) Stop(_ context.Context, id string, _ time.Duration) error {
	f.Mu.Lock()
	c := f.Find(id)
	if c != nil {
		c.Info.Running, c.Info.Status, c.Info.ExitCode = false, "exited", 143
	}
	f.Mu.Unlock()
	if c != nil {
		f.Die(c.Info.ID)
	}
	return nil
}

func (f *FakeEngine) Remove(_ context.Context, id string) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if c := f.Find(id); c != nil {
		delete(f.Containers, c.Info.ID)
	}
	return nil
}

func (f *FakeEngine) Inspect(_ context.Context, id string) (podman.ContainerInfo, bool, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if c := f.Find(id); c != nil {
		return c.Info, true, nil
	}
	return podman.ContainerInfo{}, false, nil
}

func (f *FakeEngine) List(_ context.Context, label string) ([]podman.ContainerInfo, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	var out []podman.ContainerInfo
	for _, c := range f.Containers {
		if _, ok := c.Info.Labels[label]; ok {
			out = append(out, c.Info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *FakeEngine) Events(ctx context.Context, _ string, fn func(id string)) error {
	f.Mu.Lock()
	f.nextL++
	id := f.nextL
	f.listeners[id] = fn
	f.Mu.Unlock()
	<-ctx.Done()
	f.Mu.Lock()
	delete(f.listeners, id)
	f.Mu.Unlock()
	return nil
}

func (f *FakeEngine) Die(id string) {
	f.Mu.Lock()
	ls := make([]func(string), 0, len(f.listeners))
	for _, l := range f.listeners {
		ls = append(ls, l)
	}
	f.Mu.Unlock()
	for _, l := range ls {
		l(id)
	}
}

// exit makes a container's process end with code; restarting marks it as
// being restarted by the engine.
func (f *FakeEngine) Exit(name string, code int, restarting bool) {
	f.Mu.Lock()
	c := f.Find(name)
	c.Info.Running, c.Info.Restarting, c.Info.ExitCode, c.Info.Status = false, restarting, code, "exited"
	id := c.Info.ID
	f.Mu.Unlock()
	f.Die(id)
}

func (f *FakeEngine) Watching() int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return len(f.listeners)
}
