package apps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/modules/apps/catalog"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestLocalStatusURL(t *testing.T) {
	for u, want := range map[string]bool{
		"http://127.0.0.1:8481/status":   true,
		"http://localhost:8481/status":   true,
		"http://192.168.18.33:8481/":     true,
		"https://10.0.0.5/status":        true,
		"http://[::1]:8481/status":       true,
		"http://example.com/status":      false,
		"http://8.8.8.8/status":          false,
		"ftp://127.0.0.1/status":         false,
		"127.0.0.1:8481/status":          false,
		"http:///status":                 false,
		"http://core.local:8481/status":  false, // a name that could be anywhere
		"http://169.254.169.254/status":  true,  // link-local is the home network too
		"http://user@8.8.8.8:80/status":  false,
		"http://127.0.0.1.evil.com/path": false,
	} {
		if got := catalog.LocalStatusURL(u); got != want {
			t.Errorf("%s: %v, want %v", u, got, want)
		}
	}
}

func TestStatusInAnAppFile(t *testing.T) {
	c := catalog.Build(nil, []catalog.AppFile{
		mustAppFile(t, "/x/ok.toml", "name = \"A\"\nstatus = { url = \"http://127.0.0.1:8481/status\" }\nrunner = { type = \"exec\", command = [\"x\"] }"),
		mustAppFile(t, "/x/bad.toml", "name = \"B\"\nstatus = { url = \"http://example.com/status\" }\nrunner = { type = \"exec\", command = [\"x\"] }"),
	}, nil)
	a, _ := c.Get("ok")
	if a.Status.URL != "http://127.0.0.1:8481/status" {
		t.Fatalf("status %+v", a.Status)
	}
	if len(c.Problems) != 1 || !strings.Contains(c.Problems[0].Error, "status.url") {
		t.Fatalf("problems %+v", c.Problems)
	}
}

func mustAppFile(t *testing.T, path, text string) catalog.AppFile {
	t.Helper()
	f, err := catalog.ParseAppFile(path, []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A service's page is what it says, kept to the shape and sizes menus show;
// when it cannot be had, that is an answer too.
func TestReadingAServicesStatus(t *testing.T) {
	var serve http.HandlerFunc
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serve(w, r) }))
	defer srv.Close()
	appsDir := t.TempDir()
	write := func(name, url string) {
		text := "name = \"" + name + "\"\nsurface = \"background\"\nrunner = { type = \"exec\", command = [\"x\"] }\n"
		if url != "" {
			text += "status = { url = \"" + url + "\" }\n"
		}
		if err := os.WriteFile(filepath.Join(appsDir, strings.ToLower(name)+".toml"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Svc", srv.URL+"/status")
	write("Plain", "")
	m := New(Options{AppsDir: appsDir, LookPath: fakeLookPath, NoWatch: true})
	ctx := context.Background()
	if err := m.Start(ctx, &fakeCore{}); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	read := func() StatusAnswer {
		t.Helper()
		m.status = statusCache{} // not the moment before's answer
		v, err := m.Read(ctx, "status", map[string]string{"id": "svc"})
		if err != nil {
			t.Fatal(err)
		}
		return v.(StatusAnswer)
	}

	serve = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"title":"Svc","updated":"now","extra":1,"sections":[
			{"title":"Running now","rows":[{"title":"Fetching\n   eurodesk","badge":"running","tone":"POSITIVE","secret":"x"},
			{"title":"b","tone":"purple","value":"` + strings.Repeat("v", 500) + `"}]}]}`))
	}
	a := read()
	if !a.OK || a.Document == nil || a.Document.Title != "Svc" || len(a.Document.Sections) != 1 {
		t.Fatalf("answer %+v", a)
	}
	rows := a.Document.Sections[0].Rows
	if rows[0].Title != "Fetching eurodesk" || rows[0].Tone != "positive" || rows[1].Tone != "" || len([]rune(rows[1].Value)) != 80 {
		t.Fatalf("rows %+v", rows)
	}

	serve = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not json`)) }
	if a := read(); a.OK || !strings.Contains(a.Error, "not the status document") {
		t.Fatalf("bad json: %+v", a)
	}
	serve = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"title":"no sections"}`)) }
	if a := read(); a.OK || !strings.Contains(a.Error, "no sections") {
		t.Fatalf("no sections: %+v", a)
	}
	serve = func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }
	if a := read(); a.OK || !strings.Contains(a.Error, "500") {
		t.Fatalf("500: %+v", a)
	}
	serve = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/", http.StatusFound)
	}
	if a := read(); a.OK {
		t.Fatalf("a redirect is followed: %+v", a)
	}
	serve = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sections":[{"rows":[{"title":"` + strings.Repeat("x", statusMaxBytes) + `"}]}]}`))
	}
	if a := read(); a.OK || !strings.Contains(a.Error, "too large") {
		t.Fatalf("too large: %+v", a)
	}
	// Bounded: more sections and rows than menus show are cut.
	serve = func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString(`{"sections":[`)
		for i := 0; i < 30; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"title":"s","rows":[`)
			for j := 0; j < 150; j++ {
				if j > 0 {
					b.WriteString(",")
				}
				b.WriteString(`{"title":"r"}`)
			}
			b.WriteString(`]}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}
	if a := read(); !a.OK || len(a.Document.Sections) != statusSections || len(a.Document.Sections[0].Rows) != statusRows {
		t.Fatalf("not bounded: %+v", a)
	}
	// A service that is slow is not waited for.
	serve = func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(statusTimeout + time.Second)
		_, _ = w.Write([]byte(`{}`))
	}
	start := time.Now()
	if a := read(); a.OK || time.Since(start) > statusTimeout+time.Second {
		t.Fatalf("slow: %+v after %v", a, time.Since(start))
	}
	// Asked again at once, the answer is the same one (one fetch).
	hits := 0
	serve = func(w http.ResponseWriter, _ *http.Request) { hits++; _, _ = w.Write([]byte(`{"sections":[]}`)) }
	m.status = statusCache{}
	for range 3 {
		if _, err := m.Read(ctx, "status", map[string]string{"id": "svc"}); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Fatalf("%d fetches for three reads", hits)
	}
	// An app with no status, and none at all.
	if _, err := m.Read(ctx, "status", map[string]string{"id": "plain"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("no status: %v", err)
	}
	if _, err := m.Read(ctx, "status", map[string]string{"id": "nope"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("no app: %v", err)
	}
}
