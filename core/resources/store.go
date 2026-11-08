// Package resources is the store of declared things: one folder per kind
// under hostd's config folder, one TOML file per resource. It reads them when
// hostd starts and whenever a file changes, checks each against its kind's
// schema, and tells the module that owns the kind (sdk.Reconciler). A file
// that is wrong is rejected and the previous version stays in use, so a typo
// in an editor never takes down what worked.
package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/fsx"
	"github.com/davitizhgenti/hostd/sdk"
)

// NamePattern is what a resource's name (its file name without .toml) looks
// like: one rule for every kind.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidName reports whether s can be a resource's name.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// Options configures a Store.
type Options struct {
	// Root is the config folder; each kind lives in Root/<plural>.
	Root   string
	Logger *slog.Logger
	// Debounce is how long the watcher waits after a change before it reads
	// the folder again (an editor writes a file in several steps).
	Debounce time.Duration
}

// Store holds the resources of every kind.
type Store struct {
	e    *core.Engine
	opts Options
	log  *slog.Logger

	applyMu sync.Mutex // one change at a time: the watcher, Put and Delete

	mu      sync.Mutex
	entries map[string]*entry // by kind/name
}

// entry is one resource file and what became of it.
type entry struct {
	kind, name string
	file       string
	hash       [32]byte
	// res is the version in use (zero Spec: none has been accepted yet).
	res    sdk.Resource
	status sdk.ResourceStatus
	// problem is why the file on disk is not in use, if it is not.
	problem string
	gen     uint64
}

// Item is a resource as the API shows it.
type Item struct {
	Kind       string             `json:"kind"`
	Name       string             `json:"name"`
	File       string             `json:"file"`
	Spec       json.RawMessage    `json:"spec,omitempty"`
	Status     sdk.ResourceStatus `json:"status"`
	Generation uint64             `json:"generation"`
	// Source says where it comes from, when the owner lists it ("desktop").
	Source string `json:"source,omitempty"`
	// Error says why the file as it is on disk is not in use; Spec is then
	// the version that still is, if any.
	Error string `json:"error,omitempty"`
}

// New returns a store over the engine's modules. Call Start once they run.
func New(e *core.Engine, o Options) *Store {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Debounce == 0 {
		o.Debounce = 150 * time.Millisecond
	}
	return &Store{e: e, opts: o, log: o.Logger, entries: map[string]*entry{}}
}

func key(kind, name string) string { return kind + "/" + name }

func (s *Store) kinds() map[string]core.KindOwner {
	out := map[string]core.KindOwner{}
	for _, k := range s.e.Registry().Kinds() {
		out[k.Kind] = k
	}
	return out
}

func (s *Store) kind(kind string) (core.KindOwner, error) {
	k, ok := s.kinds()[kind]
	if !ok {
		return core.KindOwner{}, sdk.Errorf(sdk.CodeNotFound, "no kind %q", kind)
	}
	return k, nil
}

func (s *Store) dir(k core.KindOwner) string { return filepath.Join(s.opts.Root, k.Plural) }

func (s *Store) path(k core.KindOwner, name string) string {
	return filepath.Join(s.dir(k), name+".toml")
}

// Start reads every folder, applies what it finds, and follows the folders
// until ctx ends. The modules must be running.
func (s *Store) Start(ctx context.Context) error {
	for _, k := range s.e.Registry().Kinds() {
		s.scan(ctx, k)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		// Out of inotify instances, say: the files read now are in use, and
		// changes to them wait for the next start.
		s.log.Warn("not following the resource folders; changes to them need a restart of hostd", "err", err)
		return nil
	}
	byDir := map[string]core.KindOwner{}
	watch := func() {
		_ = os.MkdirAll(s.opts.Root, 0o755)
		_ = w.Add(s.opts.Root)
		for _, k := range s.e.Registry().Kinds() {
			d := s.dir(k)
			byDir[d] = k
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				_ = w.Add(d)
			}
		}
	}
	watch() // before returning: a change right after Start must be seen
	go s.follow(ctx, w, byDir, watch)
	return nil
}

// follow rescans the folder of a kind when something in it changes. The
// config folder itself is watched too, so a kind's folder made later is
// picked up.
func (s *Store) follow(ctx context.Context, w *fsnotify.Watcher, byDir map[string]core.KindOwner, watch func()) {
	defer w.Close()
	dirty := map[string]bool{}
	var timer *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			dir := filepath.Dir(ev.Name)
			if dir == s.opts.Root { // a kind's folder appeared or went
				watch()
				if k, ok := byDir[ev.Name]; ok {
					dirty[k.Kind] = true
				}
			} else if k, ok := byDir[dir]; ok && !strings.HasPrefix(filepath.Base(ev.Name), ".") {
				dirty[k.Kind] = true
			}
			if len(dirty) > 0 && timer == nil {
				timer = time.NewTimer(s.opts.Debounce)
				fire = timer.C
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			s.log.Warn("watching the resource folders", "err", err)
		case <-fire:
			timer, fire = nil, nil
			kinds := s.kinds()
			for kind := range dirty {
				if k, ok := kinds[kind]; ok {
					s.scan(ctx, k)
				}
			}
			dirty = map[string]bool{}
		}
	}
}

// scan makes the store match a kind's folder: files that are new or changed
// are applied, entries whose file is gone are deleted.
func (s *Store) scan(ctx context.Context, k core.KindOwner) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	files := map[string]string{} // name -> path
	if list, err := os.ReadDir(s.dir(k)); err == nil {
		for _, de := range list {
			n := de.Name()
			if de.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".toml") {
				continue
			}
			files[strings.TrimSuffix(n, ".toml")] = filepath.Join(s.dir(k), n)
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := os.ReadFile(files[name])
		if err != nil {
			s.reject(k, name, files[name], nil, fmt.Errorf("reading it: %w", err))
			continue
		}
		s.mu.Lock()
		e := s.entries[key(k.Kind, name)]
		same := e != nil && e.hash == sha256.Sum256(raw)
		s.mu.Unlock()
		if same {
			continue
		}
		s.load(ctx, k, name, files[name], raw)
	}
	s.mu.Lock()
	var gone []string
	for _, e := range s.entries {
		if e.kind == k.Kind {
			if _, ok := files[e.name]; !ok {
				gone = append(gone, e.name)
			}
		}
	}
	s.mu.Unlock()
	sort.Strings(gone)
	for _, name := range gone {
		s.remove(ctx, k, name)
	}
}

// parse turns a file into a resource, checked against the kind's schema.
func parse(k core.KindOwner, name string, raw []byte) (sdk.Resource, error) {
	if !ValidName(name) {
		return sdk.Resource{}, fmt.Errorf("the name %q: use lowercase letters, digits, '.', '_' and '-'", name)
	}
	var doc map[string]any
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return sdk.Resource{}, err
	}
	spec, err := json.Marshal(doc)
	if err != nil {
		return sdk.Resource{}, err
	}
	if k.Schema != nil {
		schema, err := sdk.CompileArgsSchema(k.Schema)
		if err != nil {
			return sdk.Resource{}, err
		}
		if err := schema.Validate(spec); err != nil {
			return sdk.Resource{}, errors.New(sdk.AsError(err).Message)
		}
	}
	return sdk.Resource{Kind: k.Kind, Name: name, Spec: spec, Raw: raw}, nil
}

// load parses and applies one file, and records the outcome.
func (s *Store) load(ctx context.Context, k core.KindOwner, name, file string, raw []byte) {
	hash := sha256.Sum256(raw)
	res, err := parse(k, name, raw)
	if err != nil {
		s.reject(k, name, file, &hash, err)
		return
	}
	st, err := s.apply(ctx, k, res)
	if err != nil {
		s.reject(k, name, file, &hash, err)
		return
	}
	s.mu.Lock()
	e := s.entries[key(k.Kind, name)]
	if e == nil {
		e = &entry{kind: k.Kind, name: name}
		s.entries[key(k.Kind, name)] = e
	}
	e.gen++
	res.Generation = e.gen
	e.file, e.hash, e.res, e.status, e.problem = file, hash, res, st, ""
	s.mu.Unlock()
	s.e.Announce(sdk.EventResourceApplied, map[string]any{"kind": k.Kind, "name": name, "generation": res.Generation})
}

func (s *Store) apply(ctx context.Context, k core.KindOwner, res sdk.Resource) (sdk.ResourceStatus, error) {
	rc, ok := s.e.Registry().Reconciler(k.Kind)
	if !ok {
		return sdk.ResourceStatus{}, fmt.Errorf("the %s module is not running", k.Module)
	}
	return rc.Apply(ctx, res)
}

// reject records a file that is not in use and why. The version in use
// before it, if any, stays.
func (s *Store) reject(k core.KindOwner, name, file string, hash *[32]byte, err error) {
	s.mu.Lock()
	e := s.entries[key(k.Kind, name)]
	if e == nil {
		e = &entry{kind: k.Kind, name: name}
		s.entries[key(k.Kind, name)] = e
	}
	e.file, e.problem = file, err.Error()
	if hash != nil {
		e.hash = *hash
	}
	keeping := e.res.Spec != nil
	s.mu.Unlock()
	s.log.Warn("resource file not used", "kind", k.Kind, "name", name, "keeping_previous", keeping, "err", err)
	s.e.Announce(sdk.EventResourceRejected, map[string]any{"kind": k.Kind, "name": name, "error": err.Error()})
}

// remove takes a resource whose file is gone out of use.
func (s *Store) remove(ctx context.Context, k core.KindOwner, name string) {
	s.mu.Lock()
	e := s.entries[key(k.Kind, name)]
	inUse := e != nil && e.res.Spec != nil
	delete(s.entries, key(k.Kind, name))
	s.mu.Unlock()
	if inUse {
		if rc, ok := s.e.Registry().Reconciler(k.Kind); ok {
			if err := rc.Delete(ctx, k.Kind, name); err != nil {
				s.log.Warn("a module could not remove a resource", "kind", k.Kind, "name", name, "err", err)
			}
		}
	}
	s.e.Announce(sdk.EventResourceDeleted, map[string]any{"kind": k.Kind, "name": name})
}

// item builds what the API shows of an entry, asking the owner for a live
// status if it keeps one.
func (s *Store) item(ctx context.Context, e *entry) Item {
	s.mu.Lock()
	it := Item{Kind: e.kind, Name: e.name, File: e.file, Spec: e.res.Spec, Status: e.status, Generation: e.gen, Error: e.problem}
	s.mu.Unlock()
	if rc, ok := s.e.Registry().Reconciler(e.kind); ok && it.Spec != nil {
		if sr, ok := rc.(sdk.StatusReporter); ok {
			if st, ok := sr.StatusOf(ctx, e.kind, e.name); ok {
				it.Status = st
			}
		}
	}
	return it
}

// listed turns what an owner lists into an item, with what the store knows
// of the file: its generation, and why the file on disk is not in use.
func (s *Store) listed(l sdk.Listed, kind string) Item {
	it := Item{Kind: kind, Name: l.Name, File: l.File, Spec: l.Spec, Status: l.Status, Source: l.Source}
	s.mu.Lock()
	if e := s.entries[key(kind, l.Name)]; e != nil {
		it.Generation, it.Error = e.gen, e.problem
		if it.File == "" {
			it.File = e.file
		}
	}
	s.mu.Unlock()
	return it
}

// lister is the owner's Lister, if it has one.
func (s *Store) lister(kind string) sdk.Lister {
	if rc, ok := s.e.Registry().Reconciler(kind); ok {
		if l, ok := rc.(sdk.Lister); ok {
			return l
		}
	}
	return nil
}

// List returns the resources of a kind, by name. params are the request's
// query parameters, for an owner that lists the resources itself.
func (s *Store) List(ctx context.Context, kind string, params map[string]string) ([]Item, error) {
	if _, err := s.kind(kind); err != nil {
		return nil, err
	}
	var out []Item
	seen := map[string]bool{}
	l := s.lister(kind)
	if l != nil {
		got, err := l.List(ctx, kind, params)
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			out = append(out, s.listed(g, kind))
			seen[g.Name] = true
		}
	}
	s.mu.Lock()
	var es []*entry
	for _, e := range s.entries {
		// With a lister, a file that is in use but not listed is one the
		// owner leaves out (a hidden app); one that was rejected has nothing
		// in use, so only the core can show it.
		if e.kind == kind && !seen[e.name] && (l == nil || e.res.Spec == nil) {
			es = append(es, e)
		}
	}
	s.mu.Unlock()
	sort.Slice(es, func(i, j int) bool { return es[i].name < es[j].name })
	for _, e := range es {
		out = append(out, s.item(ctx, e))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one resource.
func (s *Store) Get(ctx context.Context, kind, name string) (Item, error) {
	if _, err := s.kind(kind); err != nil {
		return Item{}, err
	}
	if l := s.lister(kind); l != nil {
		if g, ok := l.Get(ctx, kind, name); ok {
			return s.listed(g, kind), nil
		}
	}
	s.mu.Lock()
	e := s.entries[key(kind, name)]
	s.mu.Unlock()
	if e == nil {
		return Item{}, sdk.Errorf(sdk.CodeNotFound, "no %s %q", kind, name)
	}
	return s.item(ctx, e), nil
}

// Raw returns the file of a resource as written (what hostctl edit opens).
func (s *Store) Raw(kind, name string) ([]byte, error) {
	k, err := s.kind(kind)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.path(k, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, sdk.Errorf(sdk.CodeNotFound, "no %s %q", kind, name)
	}
	return b, err
}

// Put checks a resource and, unless check is set, puts it in use and writes
// its file. The body is TOML, or a JSON object (written as TOML). A resource
// that is rejected is not written, and what was there stays.
func (s *Store) Put(ctx context.Context, kind, name string, body []byte, check bool) (Item, error) {
	k, err := s.kind(kind)
	if err != nil {
		return Item{}, err
	}
	raw, err := toTOML(body)
	if err != nil {
		return Item{}, sdk.Errorf(sdk.CodeInvalidArgs, "%s %q: %v", kind, name, err)
	}
	res, err := parse(k, name, raw)
	if err != nil {
		return Item{}, sdk.Errorf(sdk.CodeInvalidArgs, "%s %q: %v", kind, name, err)
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if check {
		if rc, ok := s.e.Registry().Reconciler(k.Kind); ok {
			if ch, ok := rc.(sdk.Checker); ok {
				if err := ch.Check(ctx, res); err != nil {
					return Item{}, sdk.Errorf(sdk.CodeInvalidArgs, "%s %q: %v", kind, name, err)
				}
			}
		}
		return Item{Kind: kind, Name: name, Spec: res.Spec, File: s.path(k, name)}, nil
	}
	if fi, err := os.Lstat(s.dir(k)); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return Item{}, sdk.Errorf(sdk.CodePreconditionFailed,
			"the %s folder is a link, to a followed repository: change the %s there", k.Plural, k.Plural)
	}
	st, err := s.apply(ctx, k, res)
	if err != nil {
		return Item{}, sdk.Errorf(sdk.CodeInvalidArgs, "%s %q: %v", kind, name, err)
	}
	if err := fsx.WriteFile(s.path(k, name), raw, 0o644); err != nil {
		return Item{}, err
	}
	hash := sha256.Sum256(raw)
	s.mu.Lock()
	e := s.entries[key(kind, name)]
	if e == nil {
		e = &entry{kind: kind, name: name}
		s.entries[key(kind, name)] = e
	}
	e.gen++
	res.Generation = e.gen
	e.file, e.hash, e.res, e.status, e.problem = s.path(k, name), hash, res, st, ""
	s.mu.Unlock()
	s.e.Announce(sdk.EventResourceApplied, map[string]any{"kind": kind, "name": name, "generation": res.Generation})
	return s.item(ctx, e), nil
}

// Delete removes a resource's file and takes it out of use.
func (s *Store) Delete(ctx context.Context, kind, name string) error {
	k, err := s.kind(kind)
	if err != nil {
		return err
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	s.mu.Lock()
	e := s.entries[key(kind, name)]
	s.mu.Unlock()
	if e == nil {
		if l := s.lister(kind); l != nil {
			if g, ok := l.Get(ctx, kind, name); ok {
				return sdk.Errorf(sdk.CodeInvalidArgs, "the %s %q is not a file hostd can delete: it comes from %s", kind, g.Name, firstSet(g.Source, "the system"))
			}
		}
		return sdk.Errorf(sdk.CodeNotFound, "no %s %q", kind, name)
	}
	if fi, err := os.Lstat(s.dir(k)); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return sdk.Errorf(sdk.CodePreconditionFailed,
			"the %s folder is a link, to a followed repository: change the %s there", k.Plural, k.Plural)
	}
	if err := os.Remove(s.path(k, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.remove(ctx, k, name)
	return nil
}

// toTOML returns body as TOML: a JSON object is converted, anything else is
// taken to be TOML already.
func toTOML(body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, nil
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(numbers(doc)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// numbers turns json.Number values into int64 or float64, which the TOML
// encoder understands.
func numbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = numbers(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = numbers(e)
		}
		return x
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	}
	return v
}

func firstSet(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
