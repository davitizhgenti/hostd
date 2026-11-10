package resources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

// owner is a module that owns the kind "note" and records what it is told.
type owner struct {
	*kit.Module
	mu      sync.Mutex
	applied map[string]string // name -> text
	deleted []string
	reject  string // a text it refuses
}

type noteSpec struct {
	Text string   `json:"text"`
	Tags []string `json:"tags,omitempty"`
}

func newOwner() *owner {
	o := &owner{applied: map[string]string{}}
	m := kit.New("notes", "1").Owns("notes.*")
	kit.Action(m, kit.Spec{Type: "notes.show", Scope: sdk.ScopeRead},
		func(context.Context, kit.Ctx, struct {
			Name string `json:"name"`
		}) (struct{}, error) {
			return struct{}{}, nil
		})
	kit.Kind(m, kit.KindSpec{Kind: "note", Plural: "notes", Verbs: []sdk.VerbSpec{{Name: "show", Action: "notes.show"}}},
		func(_ context.Context, _ kit.Ctx, r kit.Res[noteSpec]) (sdk.ResourceStatus, error) {
			o.mu.Lock()
			defer o.mu.Unlock()
			if o.reject != "" && r.Spec.Text == o.reject {
				return sdk.ResourceStatus{}, errors.New("the module does not like " + r.Spec.Text)
			}
			o.applied[r.Name] = r.Spec.Text
			return sdk.ResourceStatus{Conditions: []sdk.Condition{{Type: sdk.ConditionReady, Status: sdk.ConditionTrue}}}, nil
		},
		func(_ context.Context, _ kit.Ctx, name string) error {
			o.mu.Lock()
			defer o.mu.Unlock()
			delete(o.applied, name)
			o.deleted = append(o.deleted, name)
			return nil
		})
	o.Module = m
	return o
}

func (o *owner) text(name string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	t, ok := o.applied[name]
	return t, ok
}

type rig struct {
	o      *owner
	s      *Store
	root   string
	e      *core.Engine
	events <-chan sdk.Event
}

func newRig(t *testing.T, files map[string]string) *rig {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, "notes", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o := newOwner()
	reg := core.NewRegistry()
	if err := reg.Add(o); err != nil {
		t.Fatal(err)
	}
	e := core.New(reg, core.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := New(e, Options{Root: root, Debounce: 20 * time.Millisecond})
	t.Cleanup(func() { cancel(); s.Wait(); _ = e.Stop(context.Background()) })
	events := e.Subscribe(ctx, "resource.*")
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return &rig{o: o, s: s, root: root, e: e, events: events}
}

func (r *rig) event(t *testing.T, typ string) sdk.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-r.events:
			if ev.Type == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %s event", typ)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartAppliesFilesAndReportsBadOnes(t *testing.T) {
	r := newRig(t, map[string]string{
		"hello.toml":    `text = "hi"`,
		"bad.toml":      `text = 5`,       // wrong type for the schema
		"typo.toml":     `txt = "x"`,      // unknown key: additionalProperties false
		"Bad_Name.toml": `text = "x"`,     // not a valid name
		".hidden.toml":  `text = "x"`,     // skipped
		"readme.md":     "not a resource", // skipped
	})
	if txt, ok := r.o.text("hello"); !ok || txt != "hi" {
		t.Fatalf("hello not applied: %q %v", txt, ok)
	}
	items, err := r.s.List(context.Background(), "note", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Name] = it.Error
	}
	if len(got) != 5 || got["hello"] != "" { // and "builtin", which the owner lists
		t.Fatalf("items = %v", got)
	}
	for _, name := range []string{"bad", "typo", "Bad_Name"} {
		if got[name] == "" {
			t.Errorf("%s should be rejected", name)
		}
	}
	if !strings.Contains(got["Bad_Name"], "lowercase") {
		t.Errorf("name error = %q", got["Bad_Name"])
	}
	it, _ := r.s.Get(context.Background(), "note", "hello")
	if !it.Status.Ready() || it.Generation != 1 || string(it.Spec) != `{"text":"hi"}` {
		t.Fatalf("hello = %+v", it)
	}
	if _, err := r.s.Get(context.Background(), "note", "nope"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := r.s.List(context.Background(), "ghost", nil); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestFileChangesAreFollowed(t *testing.T) {
	r := newRig(t, map[string]string{"a.toml": `text = "one"`})
	r.o.mu.Lock()
	r.o.reject = "poison"
	r.o.mu.Unlock()
	file := filepath.Join(r.root, "notes", "a.toml")

	// An edit is applied.
	if err := os.WriteFile(file, []byte(`text = "two"`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the edit", func() bool { txt, _ := r.o.text("a"); return txt == "two" })

	// A rejected edit leaves the version in use, and says why.
	if err := os.WriteFile(file, []byte(`text = "poison"`), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := r.event(t, sdk.EventResourceRejected)
	if !strings.Contains(string(ev.Data), "does not like poison") {
		t.Fatalf("rejected event = %s", ev.Data)
	}
	if txt, _ := r.o.text("a"); txt != "two" {
		t.Fatalf("a rejected file replaced the one in use: %q", txt)
	}
	it, _ := r.s.Get(context.Background(), "note", "a")
	if it.Error == "" || string(it.Spec) != `{"text":"two"}` {
		t.Fatalf("a = %+v", it)
	}

	// A new file appears, in a folder that did not exist for another kind's sake.
	if err := os.WriteFile(filepath.Join(r.root, "notes", "b.toml"), []byte(`text = "bee"`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new file", func() bool { txt, _ := r.o.text("b"); return txt == "bee" })

	// Deleting the file deletes the resource.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the deletion", func() bool { _, ok := r.o.text("a"); return !ok })
	r.event(t, sdk.EventResourceDeleted)
	if _, err := r.s.Get(context.Background(), "note", "a"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("deleted resource still there: %v", err)
	}
}

func TestPutCheckAndDelete(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	r.o.mu.Lock()
	r.o.reject = "poison"
	r.o.mu.Unlock()

	// A check writes nothing and applies nothing.
	if _, err := r.s.Put(ctx, "note", "c", []byte(`text = "x"`), true); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.o.text("c"); ok {
		t.Fatal("check applied the resource")
	}
	if _, err := os.Stat(filepath.Join(r.root, "notes", "c.toml")); err == nil {
		t.Fatal("check wrote a file")
	}

	// Put from TOML, then from JSON.
	it, err := r.s.Put(ctx, "note", "c", []byte(`text = "from toml"`), false)
	if err != nil || it.Generation != 1 || !it.Status.Ready() {
		t.Fatalf("put: %v %+v", err, it)
	}
	if txt, _ := r.o.text("c"); txt != "from toml" {
		t.Fatalf("applied %q", txt)
	}
	if _, err := r.s.Put(ctx, "note", "c", []byte(`{"text":"from json","tags":["a","b"]}`), false); err != nil {
		t.Fatal(err)
	}
	raw, err := r.s.Raw("note", "c")
	if err != nil || !strings.Contains(string(raw), `text = "from json"`) {
		t.Fatalf("file = %q %v", raw, err)
	}
	it, _ = r.s.Get(ctx, "note", "c")
	if it.Generation != 2 {
		t.Fatalf("generation = %d", it.Generation)
	}

	// Refusals write nothing and leave what was there.
	for name, body := range map[string]string{
		"schema": `text = 5`, "module": `text = "poison"`, "syntax": `text = `,
	} {
		_, err := r.s.Put(ctx, "note", "c", []byte(body), false)
		if sdk.CodeOf(err) != sdk.CodeInvalidArgs {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if raw, _ := r.s.Raw("note", "c"); !strings.Contains(string(raw), "from json") {
		t.Fatalf("a refused put changed the file: %q", raw)
	}
	if _, err := r.s.Put(ctx, "note", "Bad Name", []byte(`text = "x"`), false); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := r.s.Put(ctx, "ghost", "x", nil, false); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown kind: %v", err)
	}

	// Delete.
	if err := r.s.Delete(ctx, "note", "c"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.o.text("c"); ok {
		t.Fatal("still applied after delete")
	}
	if err := r.s.Delete(ctx, "note", "c"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAFolderMadeLaterIsFollowed(t *testing.T) {
	r := newRig(t, nil) // no notes/ folder yet
	dir := filepath.Join(r.root, "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "late.toml"), []byte(`text = "late"`), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the file in the new folder", func() bool { txt, _ := r.o.text("late"); return txt == "late" })
}

// List and Get make the owner's own items part of the kind: "builtin" is not
// a file, and is known by an alias too.
func (o *owner) List(_ context.Context, _ string, params map[string]string) ([]sdk.Listed, error) {
	out := []sdk.Listed{{Name: "builtin", Spec: []byte(`{"text":"always there"}`), Source: "system"}}
	o.mu.Lock()
	defer o.mu.Unlock()
	for name, text := range o.applied {
		out = append(out, sdk.Listed{Name: name, Spec: []byte(`{"text":"` + text + `"}`), Source: "file",
			Status: sdk.ResourceStatus{Conditions: []sdk.Condition{{Type: sdk.ConditionReady, Status: sdk.ConditionTrue}}}})
	}
	if params["hide"] == "true" { // the owner leaves out what is not "interesting"
		return nil, nil
	}
	return out, nil
}

func (o *owner) Get(ctx context.Context, kind, name string) (sdk.Listed, bool) {
	if name == "bi" {
		name = "builtin"
	}
	items, _ := o.List(ctx, kind, nil)
	for _, l := range items {
		if l.Name == name {
			return l, true
		}
	}
	return sdk.Listed{}, false
}

func TestOwnerListedItems(t *testing.T) {
	r := newRig(t, map[string]string{"hello.toml": `text = "hi"`, "bad.toml": `text = 5`})
	ctx := context.Background()

	items, err := r.s.List(ctx, "note", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Name] = it.Source + "/" + it.Error
	}
	// The owner's items, the file's generation, and a rejected file the
	// owner never saw: all in one list, by name.
	if len(items) != 3 || got["builtin"] != "system/" || got["hello"] != "file/" || !strings.HasPrefix(got["bad"], "/") || got["bad"] == "/" {
		t.Fatalf("items = %v", got)
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].Name > items[i].Name {
			t.Fatalf("not sorted: %v", got)
		}
	}
	for _, it := range items {
		if it.Name == "hello" && (it.Generation != 1 || it.File == "") {
			t.Errorf("hello: %+v", it)
		}
	}
	// A parameter reaches the owner. What it leaves out stays out, even if it
	// is a file in use; a file that was rejected is shown all the same.
	hidden, _ := r.s.List(ctx, "note", map[string]string{"hide": "true"})
	if len(hidden) != 1 || hidden[0].Name != "bad" || hidden[0].Error == "" {
		t.Errorf("with the owner listing nothing: %+v", hidden)
	}

	// Get by an alias returns the canonical name; a missing one is not found.
	it, err := r.s.Get(ctx, "note", "bi")
	if err != nil || it.Name != "builtin" || it.Source != "system" {
		t.Fatalf("alias: %+v %v", it, err)
	}
	if _, err := r.s.Get(ctx, "note", "nothing"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := r.s.Raw("note", "builtin"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("an item with no file has no file: %v", err)
	}

	// There is no file to delete for it, and it says why.
	err = r.s.Delete(ctx, "note", "builtin")
	if sdk.CodeOf(err) != sdk.CodeInvalidArgs || !strings.Contains(err.Error(), "comes from system") {
		t.Fatalf("delete: %v", err)
	}
	if err := r.s.Delete(ctx, "note", "nothing"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("delete missing: %v", err)
	}
}

// A kind's folder that is a link (a followed repository) is read, not written.
func TestAFolderThatIsALinkIsNotWritten(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	_ = os.WriteFile(filepath.Join(repo, "linked.toml"), []byte(`text = "from the repo"`), 0o644)
	if err := os.Symlink(repo, filepath.Join(root, "notes")); err != nil {
		t.Fatal(err)
	}
	o := newOwner()
	reg := core.NewRegistry()
	if err := reg.Add(o); err != nil {
		t.Fatal(err)
	}
	e := core.New(reg, core.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := New(e, Options{Root: root, Debounce: 20 * time.Millisecond})
	t.Cleanup(func() { cancel(); s.Wait(); _ = e.Stop(context.Background()) })
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if txt, ok := o.text("linked"); !ok || txt != "from the repo" {
		t.Fatalf("a file in the linked folder is read: %q %v", txt, ok)
	}
	if _, err := s.Put(ctx, "note", "new", []byte(`text = "x"`), false); sdk.CodeOf(err) != sdk.CodePreconditionFailed || !strings.Contains(err.Error(), "followed repository") {
		t.Fatalf("put: %v", err)
	}
	if err := s.Delete(ctx, "note", "linked"); sdk.CodeOf(err) != sdk.CodePreconditionFailed {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.toml")); err == nil {
		t.Fatal("wrote into the repository")
	}
	if _, err := os.Stat(filepath.Join(repo, "linked.toml")); err != nil {
		t.Fatal("removed a file of the repository")
	}
}
