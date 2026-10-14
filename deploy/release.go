package deploy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Release is a build published at URL (a GitHub release, or file:// in
// tests): SHA256SUMS, its signature SHA256SUMS.sig, and the files listed.
type Release struct {
	URL string
	// HTTP fetches the files (default: a client with a timeout); Retries
	// and Wait are for a network that is not quite up yet (default 6, 5s).
	HTTP    *http.Client
	Retries int
	Wait    time.Duration
}

// Fetch downloads names into dir.
func (r Release) Fetch(ctx context.Context, dir string, names ...string) error {
	for _, n := range names {
		if err := r.fetch(ctx, filepath.Join(dir, n), r.URL+"/"+n); err != nil {
			return err
		}
	}
	return nil
}

func (r Release) fetch(ctx context.Context, dest, url string) error {
	if path, ok := strings.CutPrefix(url, "file://"); ok {
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("no %s in the build at %s", filepath.Base(path), r.URL)
		}
		return os.WriteFile(dest, b, 0o644)
	}
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	tries, wait := r.Retries, r.Wait
	if tries == 0 {
		tries = 6
	}
	if wait == 0 {
		wait = 5 * time.Second
	}
	var last error
	for i := 0; i <= tries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		if res.StatusCode == http.StatusNotFound {
			res.Body.Close()
			return fmt.Errorf("no %s in the build at %s (CI publishes one a few minutes after each push to main; a stable channel needs a release)",
				filepath.Base(dest), r.URL)
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			last = fmt.Errorf("%s: %s", url, res.Status)
			continue
		}
		f, err := os.Create(dest)
		if err != nil {
			res.Body.Close()
			return err
		}
		_, err = io.Copy(f, res.Body)
		res.Body.Close()
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			return nil
		}
		last = err
	}
	return fmt.Errorf("cannot download %s: %w", url, last)
}

// Verify checks that SHA256SUMS in dir is signed with key (nil: hostd's
// release key) and lists each of names with the checksum it has in dir.
// With no names it checks only the signature.
func Verify(dir string, key []byte, names ...string) error {
	if key == nil {
		key = releaseKey
	}
	pub, err := parseKey(key)
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS.sig"))
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, sums, sig) {
		return errors.New("the build is not signed with hostd's key: not installing it")
	}
	listed := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if sum, name, ok := strings.Cut(sc.Text(), "  "); ok {
			listed[name] = sum
		}
	}
	for _, n := range names {
		want, ok := listed[n]
		if !ok {
			return fmt.Errorf("the signed SHA256SUMS does not list %s", n)
		}
		got, err := sha256File(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s does not match the signed SHA256SUMS", n)
		}
	}
	return nil
}

// Lists reports whether SHA256SUMS in dir lists name.
func Lists(dir, name string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	return err == nil && bytes.Contains(b, []byte("  "+name+"\n"))
}

func parseKey(pemKey []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, errors.New("the release key is not a PEM public key")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("the release key is not an Ed25519 key")
	}
	return pub, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
