package apps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestServiceWaitsForItsHealthCheck(t *testing.T) {
	var ok atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !ok.Load() {
			http.Error(w, "warming up", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	sd := newFakeSystemd()
	clk := clock.NewFake(time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC))
	r := newRigClock(t, sd, newFakeDocker(), fakeSession(t), clk)
	writeApps(t, r.m.opts.AppsDir, `
-- site.toml --
runner = { type = "process", command = ["site"] }
restart = "always"
[health]
http = "`+srv.URL+`"
start = "30s"
every = "5s"
`)
	r.m.rescan()
	data := r.start(t, "site")
	if data["state"] != "starting" {
		t.Fatalf("started %v: should wait for its health check", data)
	}
	sd.mu.Lock()
	spec := sd.started[len(sd.started)-1]
	sd.mu.Unlock()
	if !spec.Service || spec.Restart != "always" {
		t.Fatalf("unit %+v", spec)
	}
	r.next(t) // starting
	clk.Advance(2 * time.Second)
	ok.Store(true)
	var ev string
	waitUntil(t, func() bool {
		clk.Advance(time.Second)
		select {
		case e := <-r.events:
			ev = e.Type
			return true
		default:
			return false
		}
	})
	if ev != EventStarted {
		t.Fatalf("event %s, want %s", ev, EventStarted)
	}

	// Failing 3 checks in a row: unhealthy; passing again: healthy.
	ok.Store(false)
	waitUntil(t, func() bool {
		clk.Advance(5 * time.Second)
		select {
		case e := <-r.events:
			ev = e.Type
			return true
		default:
			return false
		}
	})
	if ev != EventUnhealthy {
		t.Fatalf("event %s, want %s", ev, EventUnhealthy)
	}
	ok.Store(true)
	waitUntil(t, func() bool {
		clk.Advance(5 * time.Second)
		select {
		case e := <-r.events:
			ev = e.Type
			return true
		default:
			return false
		}
	})
	if ev != EventHealthy {
		t.Fatalf("event %s, want %s", ev, EventHealthy)
	}
}

func TestServiceThatNeverBecomesHealthyFails(t *testing.T) {
	sd := newFakeSystemd()
	clk := clock.NewFake(time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC))
	r := newRigClock(t, sd, newFakeDocker(), fakeSession(t), clk)
	writeApps(t, r.m.opts.AppsDir, `
-- broken.toml --
runner = { type = "process", command = ["broken"] }
[health]
tcp = "127.0.0.1:1"
start = "10s"
`)
	r.m.rescan()
	r.start(t, "broken")
	r.next(t) // starting
	var failed bool
	waitUntil(t, func() bool {
		clk.Advance(3 * time.Second)
		select {
		case e := <-r.events:
			failed = e.Type == EventFailed && strings.Contains(string(e.Data), "health check did not pass within 10s")
			return true
		default:
			return false
		}
	})
	if !failed {
		t.Fatal("not failed for its health check")
	}
	sd.mu.Lock()
	u := sd.units[unitName("broken")]
	sd.mu.Unlock()
	if u != nil && u.info.SubState == "running" {
		t.Fatalf("the unit still runs: %+v", u.info)
	}
	if _, err := r.do(t, "instance.stop", "broken"); err == nil {
		t.Fatal("the failed instance is still live")
	}
}

func TestReleaseStart(t *testing.T) {
	// A deploy starts a release: a new instance of a running service, in
	// its own directory, with its own PORT, checked on that port.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	sd := newFakeSystemd()
	clk := clock.NewFake(time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC))
	r := newRigClock(t, sd, newFakeDocker(), fakeSession(t), clk)
	writeApps(t, r.m.opts.AppsDir, `
-- site.toml --
runner = { type = "process", command = ["./site"] }
[source]
type = "push"
port = 8080
[health]
http = "http://127.0.0.1:{port}/"
`)
	r.m.rescan()
	app, _ := r.m.catalog().Get("site")
	if app.Deploy == nil || app.Deploy.Port != 8080 {
		t.Fatalf("deploy settings %+v", app.Deploy)
	}
	start := func(dir string) map[string]any {
		res, err := r.e.Submit(context.Background(), sdk.Action{Type: "app.start",
			Args:   sdk.MustJSON(contract.AppStart{ID: "site", New: true, Dir: dir, Env: map[string]string{"PORT": port}}),
			Source: sdk.Source{Kind: sdk.SourceManual}}, admin)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		_ = json.Unmarshal(res.Data, &data)
		return data
	}
	first := start("/releases/site/aaa")
	second := start("/releases/site/bbb") // while the first runs: a new instance
	if first["instance"] != "site" || second["instance"] != "site#2" {
		t.Fatalf("instances %v %v", first, second)
	}
	sd.mu.Lock()
	spec := sd.started[len(sd.started)-1]
	sd.mu.Unlock()
	if spec.Dir != "/releases/site/bbb" || !slices.Contains(spec.Env, "PORT="+port) {
		t.Fatalf("release unit %+v", spec)
	}
	// Healthy on its own port.
	waitUntil(t, func() bool {
		clk.Advance(time.Second)
		in, err := r.m.readInstance("site#2")
		return err == nil && in.State == StateRunning
	})
}
