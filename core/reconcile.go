package core

import (
	"context"
	"fmt"
	"time"

	"github.com/davitizhgenti/hostd/core/audit"
	"github.com/davitizhgenti/hostd/sdk"
)

// ResourceKey is the key a resource is locked and versioned by:
// "<kind>:<name>", the key its owner's actions on it lock too (app:firefox,
// scene:evening).
func ResourceKey(kind, name string) string { return kind + ":" + name }

// resourceWait is how long a change to a resource waits for an action on
// it to finish. Files are applied one at a time, so a long action (an
// image pull) must not hold up every other file: after this the change is
// applied anyway.
const resourceWait = 10 * time.Second

// filesSource is where resource changes come from: the files in the
// config folder, however they were written.
var filesSource = sdk.Source{Kind: sdk.SourceExternal, Name: "config files"}

// Reconcile hands a resource to the module that owns its kind, as an action
// is handled: it waits for the resource's key (an action on the resource
// in progress finishes first), bumps the key's version, and records the
// change in the audit trail.
func (e *Engine) Reconcile(ctx context.Context, res sdk.Resource) (sdk.ResourceStatus, error) {
	rc, ok := e.reg.Reconciler(res.Kind)
	if !ok {
		return sdk.ResourceStatus{}, fmt.Errorf("no running module owns the kind %q", res.Kind)
	}
	var st sdk.ResourceStatus
	err := e.withResource(ctx, sdk.EventResourceApplied, res.Kind, res.Name, func() error {
		var err error
		st, err = rc.Apply(ctx, res)
		return err
	})
	return st, err
}

// Unreconcile tells the module that owns a kind that a resource is gone,
// the way Reconcile applies one.
func (e *Engine) Unreconcile(ctx context.Context, kind, name string) error {
	rc, ok := e.reg.Reconciler(kind)
	if !ok {
		return nil // nobody has it in use
	}
	return e.withResource(ctx, sdk.EventResourceDeleted, kind, name, func() error { return rc.Delete(ctx, kind, name) })
}

// withResource runs change holding the resource's key, then versions and
// records it under typ.
func (e *Engine) withResource(ctx context.Context, typ, kind, name string, change func() error) error {
	key := ResourceKey(kind, name)
	start := e.opts.Clock.Now()
	wait, cancel := context.WithTimeout(ctx, resourceWait)
	release, err := e.locks.acquireAll(wait, []string{key})
	cancel()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		e.log.Warn("a resource changes while an action on it runs", "resource", key)
		release = func() {}
	}
	err = change()
	release()
	entry := audit.Entry{Time: start, Type: typ, Args: sdk.MustJSON(map[string]string{"kind": kind, "name": name}),
		Source: filesSource, Resource: key, Duration: e.opts.Clock.Since(start)}
	if err != nil {
		se := sdk.AsError(err)
		entry.Status, entry.Code, entry.Reason = audit.StatusFailed, se.Code, se.Message
	} else {
		e.mu.Lock()
		e.versions[key]++
		entry.Version = e.versions[key]
		e.mu.Unlock()
		entry.Status = string(sdk.StatusApplied)
	}
	e.opts.Audit.Record(entry)
	return err
}
