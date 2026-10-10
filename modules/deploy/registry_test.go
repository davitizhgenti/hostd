package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestImageRef(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/me/blog":     "ghcr.io me/blog",
		"me/app":              "registry-1.docker.io me/app",
		"nginx":               "registry-1.docker.io library/nginx",
		"localhost:5000/x/y":  "localhost:5000 x/y",
		"127.0.0.1:5000/blog": "127.0.0.1:5000 blog",
		"registry.lan/a/b/c":  "registry.lan a/b/c",
	} {
		h, r := imageRef(in)
		if h+" "+r != want {
			t.Errorf("%s: %s %s", in, h, r)
		}
	}
	if registryURL("ghcr.io") != "https://ghcr.io" || registryURL("127.0.0.1:5000") != "http://127.0.0.1:5000" {
		t.Fatal("registry scheme")
	}
}

// fakeRegistry serves one repository's tags behind a token service; a
// private one needs user:token for the token.
type fakeRegistry struct {
	mu      sync.Mutex
	digests map[string]string // tag → digest
	private bool
	srv     *httptest.Server
}

func newFakeRegistry(t *testing.T, private bool) *fakeRegistry {
	r := &fakeRegistry{digests: map[string]string{}, private: private}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("scope") != "repository:me/blog:pull" || req.URL.Query().Get("service") != "fake" {
			http.Error(w, "bad scope", http.StatusBadRequest)
			return
		}
		if u, p, _ := req.BasicAuth(); r.private && (u != "me" || p != "pat") {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"token":"tok123"}`))
	})
	mux.HandleFunc("HEAD /v2/me/blog/manifests/{tag}", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer tok123" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.srv.URL+`/token",service="fake",scope="repository:me/blog:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(req.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		r.mu.Lock()
		d, ok := r.digests[req.PathValue("tag")]
		r.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", d)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRegistry) set(tag, digest string) {
	r.mu.Lock()
	r.digests[tag] = digest
	r.mu.Unlock()
}

func (r *fakeRegistry) image() string { return strings.TrimPrefix(r.srv.URL, "http://") + "/me/blog" }

func digestOf(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func TestRegistryDigest(t *testing.T) {
	m := New(Options{Root: t.TempDir(), HTTP: &http.Client{Timeout: 5 * time.Second}})
	ctx := context.Background()

	pub := newFakeRegistry(t, false)
	pub.set("main", digestOf('a'))
	if d, err := m.digest(ctx, pub.image(), "main", ""); err != nil || d != digestOf('a') {
		t.Fatalf("public: %q %v", d, err)
	}
	if _, err := m.digest(ctx, pub.image(), "nope", ""); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing tag: %v", err)
	}

	priv := newFakeRegistry(t, true)
	priv.set("main", digestOf('b'))
	if _, err := m.digest(ctx, priv.image(), "main", ""); err == nil || !strings.Contains(err.Error(), "token service: 401") {
		t.Fatalf("private without credentials: %v", err)
	}
	if _, err := m.digest(ctx, priv.image(), "main", "me:wrong"); err == nil {
		t.Fatal("wrong credentials accepted")
	}
	if d, err := m.digest(ctx, priv.image(), "main", "me:pat"); err != nil || d != digestOf('b') {
		t.Fatalf("private: %q %v", d, err)
	}
}
