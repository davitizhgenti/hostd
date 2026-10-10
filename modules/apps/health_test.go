package apps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
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
