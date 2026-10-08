package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/ids"
	"github.com/davitizhgenti/hostd/sdk"
)

// Events the core itself publishes.
const (
	EventActionDone     = "action.done"
	EventActionSkipped  = "action.skipped"
	EventActionFailed   = "action.failed"
	EventLoopDetected   = "action.loop_detected"
	defaultTimeout      = 30 * time.Second
	defaultHoldWindow   = 3 * time.Minute
	defaultMaxDepth     = 5
	defaultBusBuffer    = 256
	defaultChainMemory  = 10000
	defaultAuditEntries = 10000
)

// Options configure an Engine. Zero values get the design's defaults.
type Options struct {
	Clock  clock.Clock
	IDs    *ids.Generator
	Audit  Auditor
	Logger *slog.Logger

	// HoldWindow is how long an applied action holds its resources at its
	// priority. HoldWindows overrides it per key prefix (longest match),
	// e.g. {"audio.": time.Minute}.
	HoldWindow  time.Duration
	HoldWindows map[string]time.Duration

	// DefaultTimeout applies to actions whose spec sets none.
	DefaultTimeout time.Duration

	// MaxChainDepth is how many rule-triggered steps a cause chain may
	// have before the core stops it.
	MaxChainDepth int

	BusBuffer   int // events a subscriber may fall behind
	ChainMemory int // events remembered for following cause chains
}

// Engine is the core: it routes every action through the pipeline
// (validate, authorize, queue, check holds and versions, execute, publish)
// to the module that owns it, and delivers events to subscribers.
type Engine struct {
	reg     *Registry
	bus     *Bus
	locks   *keyLocks
	limiter *RuleLimiter
	opts    Options
	log     *slog.Logger

	life       context.Context // parent of every handler's context
	cancelLife context.CancelFunc
	inflight   sync.WaitGroup

	mu          sync.Mutex
	closing     bool
	versions    map[string]uint64
	holds       map[string]hold
	running     map[string]*running // in-flight actions by ID
	eventDepth  map[string]int      // event ID -> cause depth of what caused it
	eventsOrder []string            // FIFO for trimming eventDepth
}

type hold struct {
	priority int
	kind     sdk.SourceKind
	until    time.Time
}

type running struct {
	action  sdk.Action
	depth   int
	pending []sdk.Event // resource events emitted while handling
}

// chain travels in the context given to Validate and Handle, so actions a
// handler sends through Core.Do become its children.
type chain struct {
	action string
	root   sdk.Source // origin of the chain; sets the priority
	depth  int        // rule-triggered steps so far
	held   map[string]bool
	auth   Auth
}

type chainKey struct{}

type outcome struct {
	res sdk.Result
	err error
}

// New returns an engine over the modules in reg. Call Start to start them.
func New(reg *Registry, opts Options) *Engine {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.IDs == nil {
		opts.IDs = ids.NewGenerator(opts.Clock)
	}
	if opts.Audit == nil {
		opts.Audit = NewMemoryAudit(defaultAuditEntries)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HoldWindow == 0 {
		opts.HoldWindow = defaultHoldWindow
	}
	if opts.DefaultTimeout == 0 {
		opts.DefaultTimeout = defaultTimeout
	}
	if opts.MaxChainDepth == 0 {
		opts.MaxChainDepth = defaultMaxDepth
	}
	if opts.BusBuffer == 0 {
		opts.BusBuffer = defaultBusBuffer
	}
	if opts.ChainMemory == 0 {
		opts.ChainMemory = defaultChainMemory
	}
	life, cancel := context.WithCancel(context.Background())
	return &Engine{
		reg:        reg,
		bus:        NewBus(opts.BusBuffer),
		locks:      newKeyLocks(),
		limiter:    NewRuleLimiter(opts.Clock),
		opts:       opts,
		log:        opts.Logger,
		life:       life,
		cancelLife: cancel,
		versions:   map[string]uint64{},
		holds:      map[string]hold{},
		running:    map[string]*running{},
		eventDepth: map[string]int{},
	}
}

// Start resolves the module order and starts every module.
func (e *Engine) Start(ctx context.Context) error {
	if len(e.reg.Order()) == 0 {
		if err := e.reg.Resolve(); err != nil {
			return err
		}
	}
	return e.reg.StartAll(ctx, func(name string) sdk.Core { return moduleCore{e: e, name: name} })
}

// Stop refuses new actions, waits for running ones until ctx ends, cancels
// what is left, and stops the modules in reverse order.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	e.closing = true
	e.mu.Unlock()

	drained := make(chan struct{})
	go func() { e.inflight.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-ctx.Done():
		e.log.Warn("stopping with actions still running")
	}
	e.cancelLife()
	err := e.reg.StopAll(ctx)
	select {
	case <-drained:
	case <-ctx.Done():
	}
	return err
}

// Submit sends an action through the pipeline on behalf of a caller with
// the given authorization. The source kind must be set and may not be
// external.
func (e *Engine) Submit(ctx context.Context, a sdk.Action, auth Auth) (sdk.Result, error) {
	return e.submit(ctx, a, auth, nil)
}

// Subscribe delivers events matching filter until ctx is done.
func (e *Engine) Subscribe(ctx context.Context, filter string) <-chan sdk.Event {
	return e.bus.Subscribe(ctx, filter)
}

// Limiter is the rule rate limiter the automation module consults.
func (e *Engine) Limiter() *RuleLimiter { return e.limiter }

// Registry returns the module registry.
func (e *Engine) Registry() *Registry { return e.reg }

// Version returns a resource key's current version.
func (e *Engine) Version(key string) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.versions[key]
}

func (e *Engine) submit(ctx context.Context, a sdk.Action, auth Auth, parent *chain) (sdk.Result, error) {
	if a.ID == "" {
		a.ID = e.opts.IDs.New(ids.Action)
	}
	start := e.opts.Clock.Now()

	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return sdk.Result{}, e.fail(a, start, sdk.Errorf(sdk.CodeModuleUnavailable, "hostd is shutting down"))
	}
	e.inflight.Add(1)
	e.mu.Unlock()
	handedOff := false
	defer func() {
		if !handedOff {
			e.inflight.Done()
		}
	}()

	// 1. Validate the request itself.
	m, spec, ok := e.reg.lookup(a.Type)
	if !ok {
		return sdk.Result{}, e.fail(a, start, sdk.Errorf(sdk.CodeNotFound, "no module handles %q", a.Type))
	}
	if !a.Source.Kind.Valid() || a.Source.Kind == sdk.SourceExternal {
		return sdk.Result{}, e.fail(a, start, sdk.Errorf(sdk.CodeInvalidArgs, "source kind %q cannot send actions", a.Source.Kind))
	}
	if err := m.schemas[a.Type].Validate(a.Args); err != nil {
		return sdk.Result{}, e.fail(a, start, err)
	}

	// Place it in its chain.
	c := &chain{action: a.ID, auth: auth, held: map[string]bool{}}
	if parent != nil {
		a.Parent = parent.action
		c.root, c.depth, c.auth = parent.root, parent.depth, parent.auth
		if e.isRunning(parent.action) { // keys are only reusable while the parent holds them
			for k := range parent.held {
				c.held[k] = true
			}
		}
	} else {
		c.root = a.Source
		if a.Cause != "" {
			c.depth = e.causeDepth(a.Cause) + 1
		}
	}
	if c.depth > e.opts.MaxChainDepth {
		err := sdk.Errorf(sdk.CodeLoopDetected, "cause chain is deeper than %d steps", e.opts.MaxChainDepth)
		e.publish(sdk.Event{Type: EventLoopDetected, Action: a.ID, Source: &a.Source,
			Data: sdk.MustJSON(map[string]any{"cause": a.Cause, "depth": c.depth})}, c.depth)
		return sdk.Result{}, e.fail(a, start, err)
	}

	// 2. Authorize.
	if err := c.auth.authorize(spec, a.Args); err != nil {
		return sdk.Result{}, e.fail(a, start, err)
	}

	keys, err := sdk.ExpandKeys(spec.Keys, a.Args)
	if err != nil {
		return sdk.Result{}, e.fail(a, start, err)
	}
	if a.ExpectVersion != nil && len(keys) != 1 {
		return sdk.Result{}, e.fail(a, start, sdk.Errorf(sdk.CodeInvalidArgs,
			"expect_version needs an action on exactly one resource; %s locks %d", a.Type, len(keys)))
	}

	if spec.Slow {
		handedOff = true
		go func() {
			defer e.inflight.Done()
			_, _ = e.run(e.life, m, spec, a, c, keys, start)
		}()
		res := sdk.Result{Action: a.ID, Status: sdk.StatusAccepted}
		e.record(a, start, res, nil, "")
		return res, nil
	}
	return e.run(ctx, m, spec, a, c, keys, start)
}

// run queues for the action's keys, makes the last-moment checks, and
// executes it.
func (e *Engine) run(ctx context.Context, m *module, spec sdk.ActionSpec, a sdk.Action, c *chain, keys []string, start time.Time) (sdk.Result, error) {
	// 3. Queue: take the keys the chain does not hold yet.
	var need []string
	for _, k := range keys {
		if !c.held[k] {
			need = append(need, k)
		}
	}
	release, err := e.locks.acquireAll(ctx, need)
	if err != nil {
		return sdk.Result{}, e.fail(a, start, sdk.Errorf(sdk.CodeOf(err), "gave up waiting for %s: %v", strings.Join(need, ", "), err))
	}
	for _, k := range keys {
		c.held[k] = true
	}
	hctx, hcancel := context.WithCancel(context.WithValue(e.life, chainKey{}, c))
	abort := func(err error) (sdk.Result, error) {
		hcancel()
		release()
		return sdk.Result{}, e.fail(a, start, err)
	}

	// 4. Last-moment checks, now that nothing else can touch the keys.
	if !e.reg.started(m) {
		return abort(sdk.Errorf(sdk.CodeModuleUnavailable, "module %q is not running", m.man.Name))
	}
	if res, held := e.checkHolds(a, keys, c.root.Kind.Priority()); held {
		hcancel()
		release()
		e.record(a, start, res, nil, "")
		e.publish(sdk.Event{Type: EventActionSkipped, Action: a.ID, Source: &a.Source, Data: sdk.MustJSON(res)}, c.depth)
		return res, nil
	}
	if a.ExpectVersion != nil {
		if v := e.Version(keys[0]); v != *a.ExpectVersion {
			return abort(sdk.Errorf(sdk.CodePreconditionFailed,
				"%s is at version %d, not %d", keys[0], v, *a.ExpectVersion))
		}
	}
	if err := m.mod.Validate(hctx, a); err != nil {
		return abort(err)
	}

	// 5. Execute. The keys stay held until Handle returns, even past the
	// timeout, so nothing races a handler that is still working.
	e.mu.Lock()
	e.running[a.ID] = &running{action: a, depth: c.depth}
	e.mu.Unlock()

	out := make(chan outcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				out <- outcome{err: sdk.Errorf(sdk.CodeInternal, "module %q panicked: %v", m.man.Name, p)}
			}
		}()
		res, err := m.mod.Handle(hctx, a)
		out <- outcome{res, err}
	}()

	timeout := time.Duration(spec.Timeout)
	if timeout == 0 {
		timeout = e.opts.DefaultTimeout
	}
	timer := e.opts.Clock.NewTimer(timeout)
	select {
	case o := <-out:
		timer.Stop()
		hcancel()
		return e.finish(a, c, keys, o, release, start)
	case <-timer.C():
	}

	// Timed out: answer the caller now, report the outcome later. If the
	// handler is still busy after another timeout, cancel its context.
	e.inflight.Add(1)
	go func() {
		defer e.inflight.Done()
		hard := e.opts.Clock.NewTimer(timeout)
		var o outcome
		select {
		case o = <-out:
			hard.Stop()
		case <-hard.C():
			hcancel()
			o = <-out
		}
		hcancel()
		_, _ = e.finish(a, c, keys, o, release, start)
	}()
	err = &sdk.Error{Code: sdk.CodeTimeout, Action: a.ID,
		Message: fmt.Sprintf("%s did not finish within %s; its outcome will be published as an event", a.Type, timeout)}
	return sdk.Result{}, err
}

// finish records the outcome: versions and holds for applied actions, the
// audit entry, the events the handler emitted, and action.done or
// action.failed. It releases the keys.
func (e *Engine) finish(a sdk.Action, c *chain, keys []string, o outcome, release func(), start time.Time) (sdk.Result, error) {
	defer release()
	now := e.opts.Clock.Now()

	e.mu.Lock()
	r := e.running[a.ID]
	delete(e.running, a.ID)
	pending := r.pending

	if o.err != nil {
		for i := range pending {
			pending[i].Version = e.versions[pending[i].Resource]
		}
		e.mu.Unlock()
		err := *sdk.AsError(o.err)
		err.Action = a.ID
		e.record(a, start, sdk.Result{}, &err, "")
		e.publishPending(pending, c.depth)
		e.publish(sdk.Event{Type: EventActionFailed, Action: a.ID, Source: &a.Source, Data: sdk.MustJSON(err)}, c.depth)
		return sdk.Result{}, &err
	}

	res := o.res
	res.Action = a.ID
	if res.Status == "" {
		res.Status = sdk.StatusApplied
	}
	if res.Status == sdk.StatusApplied {
		p := c.root.Kind.Priority()
		for _, k := range keys {
			e.versions[k]++
			if p > 0 {
				e.holds[k] = hold{priority: p, kind: c.root.Kind, until: now.Add(e.holdWindow(k))}
			}
		}
		if len(keys) == 1 {
			res.Version = e.versions[keys[0]]
		}
	}
	for i := range pending {
		pending[i].Version = e.versions[pending[i].Resource]
	}
	e.mu.Unlock()

	resource := ""
	if len(keys) == 1 {
		resource = keys[0]
	}
	e.record(a, start, res, nil, resource)
	e.publishPending(pending, c.depth)
	e.publish(sdk.Event{Type: EventActionDone, Action: a.ID, Source: &a.Source, Data: sdk.MustJSON(res)}, c.depth)
	return res, nil
}

// checkHolds returns a skipped result if any key is held at a higher
// priority than p.
func (e *Engine) checkHolds(a sdk.Action, keys []string, p int) (sdk.Result, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.opts.Clock.Now()
	for _, k := range keys {
		h, ok := e.holds[k]
		if !ok {
			continue
		}
		if !now.Before(h.until) {
			delete(e.holds, k)
			continue
		}
		if p < h.priority {
			until := h.until
			return sdk.Result{Action: a.ID, Status: sdk.StatusSkipped, Reason: "held", HeldBy: h.kind, Until: &until}, true
		}
	}
	return sdk.Result{}, false
}

func (e *Engine) holdWindow(key string) time.Duration {
	best, bestLen := e.opts.HoldWindow, -1
	for prefix, d := range e.opts.HoldWindows {
		if strings.HasPrefix(key, prefix) && len(prefix) > bestLen {
			best, bestLen = d, len(prefix)
		}
	}
	return best
}

func (e *Engine) isRunning(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.running[id]
	return ok
}

func (e *Engine) causeDepth(eventID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.eventDepth[eventID]
}

// rememberEvent records the cause depth behind an event, so an action
// reacting to it continues the chain. Caller holds e.mu.
func (e *Engine) rememberEvent(id string, depth int) {
	e.eventDepth[id] = depth
	e.eventsOrder = append(e.eventsOrder, id)
	if over := len(e.eventsOrder) - e.opts.ChainMemory; over > 0 {
		for _, old := range e.eventsOrder[:over] {
			delete(e.eventDepth, old)
		}
		e.eventsOrder = append(e.eventsOrder[:0], e.eventsOrder[over:]...)
	}
}

// emit handles Core.Emit from a module.
func (e *Engine) emit(moduleName string, ev sdk.Event) {
	if !e.reg.CanEmit(moduleName, ev.Type) {
		e.log.Warn("module emitted an undeclared event; dropped", "module", moduleName, "type", ev.Type)
		return
	}
	ev.ID = e.opts.IDs.New(ids.Event)
	ev.Time = e.opts.Clock.Now()

	observed := false
	depth := 0
	e.mu.Lock()
	switch {
	case ev.Action != "":
		if r, ok := e.running[ev.Action]; ok {
			src := r.action.Source
			ev.Source = &src
			depth = r.depth
			if ev.Resource != "" { // published with its final version when the action ends
				r.pending = append(r.pending, ev)
				e.mu.Unlock()
				return
			}
		}
	case ev.Resource != "":
		// A change hostd did not make: bump the version, no hold.
		if ev.Source == nil {
			ev.Source = &sdk.Source{Kind: sdk.SourceExternal, Name: moduleName}
		}
		e.versions[ev.Resource]++
		ev.Version = e.versions[ev.Resource]
		observed = true
	}
	e.rememberEvent(ev.ID, depth)
	e.mu.Unlock()

	if observed {
		e.opts.Audit.Record(AuditEntry{
			Time: ev.Time, Event: ev.ID, Type: ev.Type, Args: ev.Data, Source: *ev.Source,
			Status: string(sdk.StatusObserved), Resource: ev.Resource, Version: ev.Version,
		})
	}
	e.bus.Publish(ev)
}

func (e *Engine) publishPending(events []sdk.Event, depth int) {
	for _, ev := range events {
		e.mu.Lock()
		e.rememberEvent(ev.ID, depth)
		e.mu.Unlock()
		e.bus.Publish(ev)
	}
}

// publish sends a core event about an action at the given cause depth, so
// a rule reacting to it continues that action's chain.
func (e *Engine) publish(ev sdk.Event, depth int) {
	ev.ID = e.opts.IDs.New(ids.Event)
	ev.Time = e.opts.Clock.Now()
	e.mu.Lock()
	e.rememberEvent(ev.ID, depth)
	e.mu.Unlock()
	e.bus.Publish(ev)
}

// fail records and publishes an action that failed before it ran, and
// returns the error with the action ID set.
func (e *Engine) fail(a sdk.Action, start time.Time, err error) error {
	se := *sdk.AsError(err)
	se.Action = a.ID
	e.record(a, start, sdk.Result{}, &se, "")
	e.publish(sdk.Event{Type: EventActionFailed, Action: a.ID, Source: &a.Source, Data: sdk.MustJSON(se)}, 0)
	return &se
}

func (e *Engine) record(a sdk.Action, start time.Time, res sdk.Result, err *sdk.Error, resource string) {
	entry := AuditEntry{
		Time: start, Action: a.ID, Type: a.Type, Args: a.Args, Source: a.Source,
		Cause: a.Cause, Parent: a.Parent, Status: string(res.Status), Reason: res.Reason,
		HeldBy: res.HeldBy, Until: res.Until, Resource: resource, Version: res.Version,
		Duration: e.opts.Clock.Since(start),
	}
	if err != nil {
		entry.Status, entry.Code, entry.Reason = StatusFailed, err.Code, err.Message
	}
	e.opts.Audit.Record(entry)
}

// moduleCore is the sdk.Core a module gets.
type moduleCore struct {
	e    *Engine
	name string
}

func (mc moduleCore) Emit(ev sdk.Event) { mc.e.emit(mc.name, ev) }

// Subscribe gives a module a stream that, unlike the bus's own, survives
// falling behind: the bus drops a slow subscriber, so this subscribes again
// at once and then passes on the bus.lagged notice, for the module to
// resync from current state (any change after that arrives as an event).
func (mc moduleCore) Subscribe(ctx context.Context, filter string) <-chan sdk.Event {
	out := make(chan sdk.Event)
	in := mc.e.bus.Subscribe(ctx, filter)
	go func() {
		defer close(out)
		for {
			ev, ok := <-in
			if !ok {
				return // ctx is done
			}
			if ev.Type == EventLagged {
				in = mc.e.bus.Subscribe(ctx, filter)
				mc.e.log.Warn("a module fell behind on events and resyncs", "module", mc.name, "filter", filter)
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (mc moduleCore) Handles(actionType string) bool {
	m, _, ok := mc.e.reg.lookup(actionType)
	return ok && mc.e.reg.started(m)
}

func (mc moduleCore) Read(ctx context.Context, module, read string, params map[string]string) (json.RawMessage, error) {
	v, err := mc.e.ModuleRead(ctx, module, read, params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// Do sends an action for a module. Called with a handler's context, the
// action joins that chain: same origin and authorization, Parent set, and
// it may reuse the keys its ancestors hold. Otherwise it is the module's
// own action, trusted, with source automation unless the module set one.
//
// A child that waits for a key held by an unrelated action, which itself
// waits for a key of the child's ancestors, would deadlock; the parent's
// timeout then reports it. Modules avoid this by only sending child actions
// on resources they own or on keys their action already holds.
func (mc moduleCore) Do(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	if c, ok := ctx.Value(chainKey{}).(*chain); ok {
		a.Source = c.root
		a.Source.Module = mc.name
		return mc.e.submit(ctx, a, c.auth, c)
	}
	if a.Source.Kind == "" {
		a.Source = sdk.Source{Kind: sdk.SourceAutomation, Name: mc.name}
	}
	a.Source.Module = mc.name
	return mc.e.submit(ctx, a, Auth{Name: mc.name, trusted: true}, nil)
}

// HeldKeys returns the keys with an active hold, for diagnostics.
func (e *Engine) HeldKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.opts.Clock.Now()
	var out []string
	for k, h := range e.holds {
		if now.Before(h.until) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ModuleState returns a module's state for the API: not_found for an
// unknown module, module_unavailable if it is not running, and an empty
// object for a module that reports no state.
func (e *Engine) ModuleState(ctx context.Context, name string) (any, error) {
	e.reg.mu.RLock()
	m, ok := e.reg.byName[name]
	e.reg.mu.RUnlock()
	if !ok {
		return nil, sdk.Errorf(sdk.CodeNotFound, "no module %q", name)
	}
	if !e.reg.started(m) {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "module %q is not running", name)
	}
	sr, ok := m.mod.(sdk.StateReporter)
	if !ok {
		return map[string]any{}, nil
	}
	return sr.State(ctx)
}

// ModuleRead answers one of a module's declared reads.
func (e *Engine) ModuleRead(ctx context.Context, moduleName, read string, params map[string]string) (any, error) {
	e.reg.mu.RLock()
	m, ok := e.reg.byName[moduleName]
	e.reg.mu.RUnlock()
	if !ok {
		return nil, sdk.Errorf(sdk.CodeNotFound, "no module %q", moduleName)
	}
	if !e.reg.started(m) {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "module %q is not running", moduleName)
	}
	r, ok := m.mod.(sdk.Reader)
	if !ok {
		return nil, sdk.Errorf(sdk.CodeNotFound, "module %q has no reads", moduleName)
	}
	return r.Read(ctx, read, params)
}
