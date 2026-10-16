package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// machine is a host named core with the given addresses, at a settable time.
type machine struct {
	addrs []netip.Addr
	now   time.Time
}

func (m *machine) opts(dir string, names ...string) Options {
	return Options{Dir: dir, Names: names,
		Hostname: func() (string, error) { return "core", nil },
		Addrs:    func() ([]netip.Addr, error) { return m.addrs, nil },
		Now:      func() time.Time { return m.now }}
}

func newMachine() *machine {
	return &machine{addrs: []netip.Addr{netip.MustParseAddr("192.168.18.33"), netip.MustParseAddr("8.8.8.8")},
		now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
}

func TestMadeOnceAndKept(t *testing.T) {
	dir, m := t.TempDir(), newMachine()
	a, err := Open(m.opts(dir))
	if err != nil {
		t.Fatal(err)
	}
	l := a.Leaf()
	if !slices.Equal(l.Names, []string{"core", "core.local"}) {
		t.Fatalf("names %v", l.Names)
	}
	// The public address is not the CA's to vouch for.
	if !slices.Equal(l.IPs, []string{"::1", "127.0.0.1", "192.168.18.33"}) {
		t.Fatalf("ips %v", l.IPs)
	}
	for name, mode := range map[string]os.FileMode{"ca-key.pem": 0o600, "key.pem": 0o600, "ca.pem": 0o644, "cert.pem": 0o644} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", name, st, err)
		}
	}
	again, err := Open(m.opts(dir))
	if err != nil || again.Fingerprint() != a.Fingerprint() {
		t.Fatalf("the CA was remade: %v", err)
	}
	if again.Leaf().NotAfter != l.NotAfter {
		t.Fatal("a current leaf was reissued")
	}
}

func TestLeafFollowsTheMachine(t *testing.T) {
	m := newMachine()
	a, err := Open(m.opts(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if renewed, _ := a.Refresh(); renewed {
		t.Fatal("renewed with nothing changed")
	}
	m.addrs = []netip.Addr{netip.MustParseAddr("192.168.18.40"), netip.MustParseAddr("100.101.102.103")} // DHCP, and Tailscale
	if renewed, err := a.Refresh(); !renewed || err != nil {
		t.Fatalf("an address change: %v %v", renewed, err)
	}
	if ips := a.Leaf().IPs; !slices.Contains(ips, "192.168.18.40") || !slices.Contains(ips, "100.101.102.103") || slices.Contains(ips, "192.168.18.33") {
		t.Fatalf("ips %v", ips)
	}
	m.now = m.now.Add(leafLife - renewLeft + time.Hour)
	if renewed, _ := a.Refresh(); !renewed {
		t.Fatal("not renewed 30 days before it expires")
	}
	if a.Leaf().NotAfter.Before(m.now.Add(leafLife - time.Hour)) {
		t.Fatal("renewed for less than a full life")
	}
}

// Names configured after the CA was made are left out, and said so.
func TestNamesTheCAWasNotMadeFor(t *testing.T) {
	dir, m := t.TempDir(), newMachine()
	if _, err := Open(m.opts(dir, "core.example.home")); err != nil {
		t.Fatal(err)
	}
	a, err := Open(m.opts(dir, "core.example.home", "core.tail1234.ts.net"))
	if err != nil {
		t.Fatal(err)
	}
	l := a.Leaf()
	if !slices.Contains(l.Names, "core.example.home") || slices.Contains(l.Names, "core.tail1234.ts.net") ||
		!slices.Equal(l.Skipped, []string{"core.tail1234.ts.net"}) {
		t.Fatalf("leaf %+v", l)
	}
}

// Whatever happens to its key, the CA cannot vouch for another site.
func TestNameConstraints(t *testing.T) {
	a, err := Open(newMachine().opts(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(a.caCert)
	for _, c := range []struct {
		dns []string
		ips []net.IP
		ok  bool
	}{
		{[]string{"core.local"}, nil, true},
		{nil, []net.IP{net.ParseIP("10.1.2.3")}, true},
		{[]string{"www.google.com"}, nil, false},
		{[]string{"evil-core.local"}, nil, false},
		{nil, []net.IP{net.ParseIP("8.8.8.8")}, false},
	} {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "x"}, NotBefore: time.Now().Add(-time.Hour),
			NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: c.dns, IPAddresses: c.ips}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, a.caCert, &key.PublicKey, a.caKey)
		if err != nil {
			t.Fatal(err)
		}
		leaf, _ := x509.ParseCertificate(der)
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots})
		if (err == nil) != c.ok {
			t.Errorf("%v %v: %v", c.dns, c.ips, err)
		}
	}
}

func TestPinnedClient(t *testing.T) {
	a, err := Open(Options{Dir: t.TempDir()}) // the real host name and addresses
	if err != nil {
		t.Fatal(err)
	}
	// As hostd serves: GetCertificate only (httptest would add its own
	// certificate, which Go prefers).
	ln, err := tls.Listen("tcp", "127.0.0.1:0", a.TLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), ReadHeaderTimeout: time.Second}
	go func() { _ = hs.Serve(ln) }()
	defer hs.Close()
	srv := struct{ URL string }{"https://" + ln.Addr().String()}
	get := func(cfg *tls.Config) error {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		res, err := c.Get(srv.URL)
		if err == nil {
			res.Body.Close()
		}
		return err
	}
	// Pinned: works by IP, though no name matches.
	if err := get(&tls.Config{InsecureSkipVerify: true, VerifyConnection: VerifyPinned(a.Fingerprint())}); err != nil { //nolint:gosec // verified by the pin
		t.Fatalf("pinned: %v", err)
	}
	other, _ := Open(Options{Dir: t.TempDir()})
	err = get(&tls.Config{InsecureSkipVerify: true, VerifyConnection: VerifyPinned(other.Fingerprint())}) //nolint:gosec // verified by the pin
	if err == nil || !strings.Contains(err.Error(), "not from the CA pinned") {
		t.Fatalf("another CA's pin: %v", err)
	}
	// What a first login shows: the server's CA.
	conn, err := tls.Dial("tcp", strings.TrimPrefix(srv.URL, "https://"), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // only reading the CA
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ca, err := ServerCA(conn.ConnectionState())
	if err != nil || Fingerprint(ca.Raw) != a.Fingerprint() {
		t.Fatalf("server CA: %v", err)
	}
	if p := Pretty(a.Fingerprint()); len(strings.Fields(p)) != 8 {
		t.Fatalf("pretty %q", p)
	}
}
