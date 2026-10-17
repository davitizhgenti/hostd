package api

import (
	"context"
	"crypto/tls"
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
// plain HTTP is sent to HTTPS (pages) or gets an error (the API).
func TestServeTLS(t *testing.T) {
	ca, err := tlsca.Open(tlsca.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "index.html"), []byte("<h1>docs</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := setup(t, Options{TLS: ca.TLSConfig(), CA: ca.CAPEM(), DocsDir: docs})
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

	plain := &http.Client{}
	code, body := get(plain, "http://"+addr+"/v1/version", e.admin)
	if code != 400 || !strings.Contains(body, "speaks HTTPS") || strings.Contains(body, `"version"`) {
		t.Fatalf("plain http: %d %s", code, body)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noRedirect.Get("http://" + addr + "/docs/install/?x=1")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMovedPermanently || res.Header.Get("Location") != "https://"+addr+"/docs/install/?x=1" {
		t.Fatalf("plain docs: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if code, body := get(pinned, "https://"+addr+"/docs/", ""); code != 200 || !strings.Contains(body, "docs") {
		t.Fatalf("docs over https: %d %s", code, body)
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
