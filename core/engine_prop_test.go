package core

import (
	"context"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// modelKey is the reference model of one resource: what the engine should
// show after any sequence of actions, outside changes and time passing.
type modelKey struct {
	value     int
	version   uint64
	holdPrio  int
	holdKind  sdk.SourceKind
	holdUntil time.Time
}

func TestPropertyHoldsAndVersions(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		fc := clock.NewFake(t0)
		res := newResModule()
		reg := NewRegistry()
		if err := reg.Add(res); err != nil {
			rt.Fatal(err)
		}
		e := New(reg, Options{Clock: fc, Audit: NewMemoryAudit(10)})
		if err := e.Start(context.Background()); err != nil {
			rt.Fatal(err)
		}
		defer func() { _ = e.Stop(context.Background()) }()

		keys := []string{"a", "b", "c"}
		kinds := []sdk.SourceKind{sdk.SourceLocal, sdk.SourceManual, sdk.SourceAutomation}
		model := map[string]*modelKey{}
		for _, k := range keys {
			model[k] = &modelKey{}
		}

		steps := rapid.IntRange(1, 60).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			k := rapid.SampledFrom(keys).Draw(rt, "key")
			m := model[k]
			switch rapid.IntRange(0, 2).Draw(rt, "op") {
			case 0: // an action
				kind := rapid.SampledFrom(kinds).Draw(rt, "kind")
				value := rapid.IntRange(1, 1000).Draw(rt, "value")
				got, err := e.Submit(context.Background(), set(k, value, kind), admin)
				if err != nil {
					rt.Fatalf("step %d: %v", i, err)
				}
				now := fc.Now()
				held := now.Before(m.holdUntil) && kind.Priority() < m.holdPrio
				if held {
					if got.Status != sdk.StatusSkipped || got.HeldBy != m.holdKind || !got.Until.Equal(m.holdUntil) {
						rt.Fatalf("step %d: %s set %s while held by %s until %v: got %+v", i, kind, k, m.holdKind, m.holdUntil, got)
					}
				} else {
					if got.Status != sdk.StatusApplied {
						rt.Fatalf("step %d: %s set %s should apply: got %+v", i, kind, k, got)
					}
					m.value = value
					m.version++
					m.holdPrio, m.holdKind, m.holdUntil = kind.Priority(), kind, now.Add(defaultHoldWindow)
					if got.Version != m.version {
						rt.Fatalf("step %d: result version %d, want %d", i, got.Version, m.version)
					}
				}
			case 1: // a change made outside hostd
				res.Core().Emit(sdk.Event{Type: "res.changed", Resource: "res:" + k})
				m.version++
			case 2: // time passes
				fc.Advance(time.Duration(rapid.Int64Range(0, int64(5*time.Minute)).Draw(rt, "advance")))
			}

			for _, kk := range keys {
				if got, want := e.Version("res:"+kk), model[kk].version; got != want {
					rt.Fatalf("step %d: version of %s = %d, want %d", i, kk, got, want)
				}
				if got, want := res.value(kk), model[kk].value; got != want {
					rt.Fatalf("step %d: value of %s = %d, want %d", i, kk, got, want)
				}
			}
		}
	})
}
