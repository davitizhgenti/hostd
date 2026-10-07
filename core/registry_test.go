package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/sdk"
)

func noCore(string) sdk.Core { return nil }

func mustAdd(t *testing.T, r *Registry, mods ...sdk.Module) {
	t.Helper()
	for _, m := range mods {
		if err := r.Add(m); err != nil {
			t.Fatalf("Add(%s): %v", m.Manifest().Name, err)
		}
	}
}

func TestRegistryStartOrderDiamond(t *testing.T) {
	// automation needs audio and apps; both need base. Registered in an
	// order that is wrong on purpose.
	log := &testutil.Log{}
	mods := []*testutil.Module{
		testutil.NewModule("automation", []string{"audio", "apps"}, nil, nil),
		testutil.NewModule("audio", []string{"base"}, nil, nil),
		testutil.NewModule("apps", []string{"base"}, nil, nil),
		testutil.NewModule("base", nil, nil, nil),
	}
	r := NewRegistry()
	for _, m := range mods {
		m.Log = log
		mustAdd(t, r, m)
	}
	if err := r.Resolve(); err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"base", "audio", "apps", "automation"}
	if got := r.Order(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("Order = %v, want %v", got, wantOrder)
	}

	ctx := context.Background()
	if err := r.StartAll(ctx, noCore); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAll(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"start base", "start audio", "start apps", "start automation",
		"stop automation", "stop apps", "stop audio", "stop base",
	}
	if got := log.Entries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle = %v\nwant        %v", got, want)
	}
}

func TestRegistryStartFailureStopsStartedModules(t *testing.T) {
	log := &testutil.Log{}
	a := testutil.NewModule("a", nil, nil, nil)
	b := testutil.NewModule("b", []string{"a"}, nil, nil)
	c := testutil.NewModule("c", []string{"b"}, nil, nil)
	b.StartFunc = func(context.Context, sdk.Core) error { return errors.New("no session") }
	r := NewRegistry()
	for _, m := range []*testutil.Module{a, b, c} {
		m.Log = log
		mustAdd(t, r, m)
	}
	if err := r.Resolve(); err != nil {
		t.Fatal(err)
	}
	err := r.StartAll(context.Background(), noCore)
	if err == nil || !strings.Contains(err.Error(), `module "b" failed to start: no session`) {
		t.Fatalf("err = %v", err)
	}
	// a was started, so it is stopped again; c never starts.
	if got, want := log.Entries(), []string{"start a", "start b", "stop a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle = %v, want %v", got, want)
	}
	// StopAll afterwards must not stop anything twice.
	if err := r.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(log.Entries()); n != 3 {
		t.Fatalf("StopAll after failed start stopped again: %v", log.Entries())
	}
}

func TestRegistryStopAllContinuesPastErrors(t *testing.T) {
	log := &testutil.Log{}
	a := testutil.NewModule("a", nil, nil, nil)
	b := testutil.NewModule("b", []string{"a"}, nil, nil)
	b.StopFunc = func(context.Context) error { return errors.New("stuck") }
	r := NewRegistry()
	for _, m := range []*testutil.Module{a, b} {
		m.Log = log
		mustAdd(t, r, m)
	}
	if err := r.Resolve(); err != nil {
		t.Fatal(err)
	}
	if err := r.StartAll(context.Background(), noCore); err != nil {
		t.Fatal(err)
	}
	err := r.StopAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), `module "b" failed to stop: stuck`) {
		t.Fatalf("err = %v", err)
	}
	if got := log.Entries(); got[len(got)-1] != "stop a" {
		t.Fatalf("a was not stopped after b failed: %v", got)
	}
}

func TestRegistryRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		mods []*testutil.Module
		want string // in the Add or Resolve error
	}{
		{
			"missing dependency",
			[]*testutil.Module{testutil.NewModule("deploy", []string{"apps"}, nil, nil)},
			`module "deploy" requires "apps", which is not enabled`,
		},
		{
			"cycle",
			[]*testutil.Module{
				testutil.NewModule("a", []string{"c"}, nil, nil),
				testutil.NewModule("b", []string{"a"}, nil, nil),
				testutil.NewModule("c", []string{"b"}, nil, nil),
				testutil.NewModule("d", nil, nil, nil),
			},
			"cycle: a, b, c",
		},
		{
			"duplicate name",
			[]*testutil.Module{testutil.NewModule("audio", nil, nil, nil), testutil.NewModule("audio", nil, nil, nil)},
			"registered twice",
		},
		{
			"overlapping namespaces",
			func() []*testutil.Module {
				a := testutil.NewModule("apps", nil, nil, nil)
				a.M.Owns = []string{"app.*", "instance.*"}
				d := testutil.NewModule("display", nil, nil, nil)
				d.M.Owns = []string{"window.*", "instance.focus"}
				return []*testutil.Module{a, d}
			}(),
			`namespace "instance.focus" overlaps "instance.*" owned by module "apps"`,
		},
		{
			"core namespace",
			func() []*testutil.Module {
				m := testutil.NewModule("sneaky", nil, nil, nil)
				m.M.Owns = []string{"action.*"}
				return []*testutil.Module{m}
			}(),
			"reserved for the core",
		},
		{
			"scope nobody declares",
			func() []*testutil.Module {
				m := testutil.NewModule("lights", nil, []string{"lights.on"}, nil)
				m.M.Actions[0].Scope = "lights"
				return []*testutil.Module{m}
			}(),
			`scope "lights" is not declared by any module`,
		},
		{
			"invalid manifest",
			func() []*testutil.Module {
				m := testutil.NewModule("bad", nil, []string{"other.thing"}, nil)
				return []*testutil.Module{m}
			}(),
			"outside the namespaces",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			var err error
			for _, m := range tc.mods {
				if err = r.Add(m); err != nil {
					break
				}
			}
			if err == nil {
				err = r.Resolve()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRegistryLookupAndEmitRights(t *testing.T) {
	audio := testutil.NewModule("audio", nil, []string{"audio.volume.set"}, []string{"audio.volume.changed"})
	apps := testutil.NewModule("apps", nil, []string{"apps.start"}, nil)
	r := NewRegistry()
	mustAdd(t, r, audio, apps)

	m, spec, ok := r.lookup("apps.start")
	if !ok || m.man.Name != "apps" || spec.Type != "apps.start" {
		t.Fatalf("lookup(apps.start) = %v %+v %v", m, spec, ok)
	}
	if _, _, ok := r.lookup("apps.stop"); ok {
		t.Fatal("lookup found an undeclared action")
	}

	if !r.CanEmit("audio", "audio.volume.changed") {
		t.Fatal("audio may emit its declared event")
	}
	for _, tc := range [][2]string{
		{"apps", "audio.volume.changed"}, // another module's event
		{"audio", "audio.muted"},         // own namespace, but undeclared
		{"ghost", "ghost.thing"},         // unknown module
	} {
		if r.CanEmit(tc[0], tc[1]) {
			t.Errorf("CanEmit(%s, %s) = true", tc[0], tc[1])
		}
	}
}

func TestRegistryFrozenAfterResolve(t *testing.T) {
	r := NewRegistry()
	mustAdd(t, r, testutil.NewModule("a", nil, nil, nil))
	if err := r.Resolve(); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(testutil.NewModule("b", nil, nil, nil)); err == nil {
		t.Fatal("Add after Resolve accepted")
	}
	if got := r.Manifests(); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("Manifests = %+v", got)
	}
}

func TestRegistryStartBeforeResolve(t *testing.T) {
	r := NewRegistry()
	mustAdd(t, r, testutil.NewModule("a", nil, nil, nil))
	if err := r.StartAll(context.Background(), noCore); err == nil {
		t.Fatal("StartAll before Resolve accepted")
	}
}

func TestRegistryScopeFromAnotherModule(t *testing.T) {
	apps := testutil.NewModule("apps", nil, nil, nil)
	apps.M.Scopes = []sdk.ScopeSpec{{Name: "apps"}}
	display := testutil.NewModule("display", nil, []string{"display.focus"}, nil)
	display.M.Actions[0].Scope = "apps"
	r := NewRegistry()
	mustAdd(t, r, apps, display)
	if err := r.Resolve(); err != nil {
		t.Fatalf("a module may use another module's scope: %v", err)
	}
}
