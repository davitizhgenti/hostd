package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/tlsca"
)

// On TCP with TLS: the API over HTTPS for a client that pinned the CA;
// plain HTTP gets the docs and an error, never the API.
func TestServeTLS(t *testing.T) {
	ca, err := tlsca.Open(tlsca.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "index.html"), []byte("<h1>docs</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := setup(t, Options{TLS: ca.TLSConfig(), CA: ca.CAPEM(), DocsDir: docs,
		TLSInfo: func() any { return map[string]string{"fingerprint": ca.Fingerprint()} }})
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := free.Addr().String()
	free.Close()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- e.srv.Serve(ctx, filepath.Join(t.TempDir(), "hostd.sock"), addr) }()
	t.Cleanup(func() { cancel(); <-served })
	waitFor(t, func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	pinned := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, VerifyConnection: tlsca.VerifyPinned(ca.Fingerprint())}}} //nolint:gosec // verified by the pin
	get := func(c *http.Client, url, token string) (int, string) {
		req, _ := http.NewRequest("GET", url, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := get(pinned, "https://"+addr+"/v1/version", e.admin); code != 200 || !strings.Contains(body, "version") {
		t.Fatalf("https: %d %s", code, body)
	}
	if code, body := get(pinned, "https://"+addr+"/v1/tls/ca.pem", ""); code != 200 || body != string(ca.CAPEM()) {
		t.Fatalf("ca.pem: %d", code)
	}
	var info map[string]string
	_, body := get(pinned, "https://"+addr+"/v1/tls", "")
	if json.Unmarshal([]byte(body), &info); info["fingerprint"] != ca.Fingerprint() {
		t.Fatalf("tls info %s", body)
	}

	plain := &http.Client{}
	code, body := get(plain, "http://"+addr+"/v1/version", e.admin)
	if code != 400 || !strings.Contains(body, "speaks HTTPS") || strings.Contains(body, `"version"`) {
		t.Fatalf("plain http: %d %s", code, body)
	}
	if code, body := get(plain, "http://"+addr+"/docs/", ""); code != 200 || !strings.Contains(body, "docs") {
		t.Fatalf("plain docs: %d %s", code, body)
	}
	// Silence on a connection does not hold up the others.
	idle, _ := net.Dial("tcp", addr)
	defer idle.Close()
	if code, _ := get(pinned, "https://"+addr+"/v1/version", e.admin); code != 200 {
		t.Fatal("an idle connection blocked the listener")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
