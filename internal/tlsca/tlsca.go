// Package tlsca is a machine's own certificate authority: HTTPS for its
// API on the home network (and its add-ons' sites), with no domain and
// no outside CA.
//
// The CA is made once and kept. It is name-constrained to this machine's
// names and to private addresses, so a device that trusts it (a browser
// with ca.pem installed) can never be fooled about any other site by it,
// even if its key leaked. It issues one leaf certificate, renewed before
// it expires and as soon as the machine's names or addresses change.
//
// Clients pin the CA by its fingerprint (VerifyPinned): they need neither
// the system's trust store nor a name that matches the address they used.
package tlsca

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	caLife    = 10 * 365 * 24 * time.Hour
	leafLife  = 397 * 24 * time.Hour // what browsers accept for a leaf
	renewLeft = 30 * 24 * time.Hour
)

// Private are the address ranges the CA may vouch for: the home network,
// Tailscale's (100.64.0.0/10, fd7a:115c:a1e0::/48 inside fc00::/7) and
// the machine itself.
var Private = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("::1/128"),
}

// Options say where the CA lives and what the machine is called.
type Options struct {
	Dir string // the CA's and the leaf's files (made 0700)
	// Names are extra DNS names for the leaf, beside the host name and
	// <host>.local. A name the CA was not made for is left out (logged by
	// the caller through Skipped).
	Names []string
	// Hostname and Addrs default to the system's.
	Hostname func() (string, error)
	Addrs    func() ([]netip.Addr, error)
	Now      func() time.Time
}

// Authority is a loaded CA and its current leaf.
type Authority struct {
	o      Options
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte

	mu      sync.Mutex
	leaf    *tls.Certificate
	leafX   *x509.Certificate
	skipped []string
}

// File names in Dir.
const (
	CAFile   = "ca.pem"
	caKey    = "ca-key.pem"
	CertFile = "cert.pem" // the leaf and the CA, for a web server (nginx, Wings)
	KeyFile  = "key.pem"
)

// Open loads the CA from Dir, or makes one, and makes sure the leaf is
// current.
func Open(o Options) (*Authority, error) {
	if o.Hostname == nil {
		o.Hostname = os.Hostname
	}
	if o.Addrs == nil {
		o.Addrs = interfaceAddrs
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, err
	}
	a := &Authority{o: o}
	if err := a.loadCA(); errors.Is(err, os.ErrNotExist) {
		if err := a.makeCA(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := a.loadLeaf(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := a.Refresh(); err != nil {
		return nil, err
	}
	return a, nil
}

// CAPEM is the CA certificate, to install on devices or pin.
func (a *Authority) CAPEM() []byte { return a.caPEM }

// Fingerprint is the CA's pin: the SHA-256 of its certificate, in hex.
func (a *Authority) Fingerprint() string { return Fingerprint(a.caCert.Raw) }

// Fingerprint formats the SHA-256 of a DER certificate as clients pin it.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// Leaf describes the current leaf certificate.
type Leaf struct {
	Names    []string  `json:"names"`
	IPs      []string  `json:"ips"`
	NotAfter time.Time `json:"not_after"`
	// Skipped are configured names the CA cannot vouch for: it was made
	// before they were configured (hostctl tls documents how to remake it).
	Skipped []string `json:"skipped,omitempty"`
}

func (a *Authority) Leaf() Leaf {
	a.mu.Lock()
	defer a.mu.Unlock()
	l := Leaf{Names: slices.Clone(a.leafX.DNSNames), NotAfter: a.leafX.NotAfter, Skipped: slices.Clone(a.skipped)}
	for _, ip := range a.leafX.IPAddresses {
		l.IPs = append(l.IPs, ip.String())
	}
	return l
}

// GetCertificate serves the current leaf (tls.Config.GetCertificate), so
// a renewed one is used without a restart.
func (a *Authority) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.leaf, nil
}

// TLSConfig is a server configuration serving the leaf.
func (a *Authority) TLSConfig() *tls.Config {
	return &tls.Config{GetCertificate: a.GetCertificate, MinVersion: tls.VersionTLS12}
}

// Refresh issues a new leaf when the current one expires within 30 days
// or names other names or addresses than the machine has now. It reports
// whether it did.
func (a *Authority) Refresh() (bool, error) {
	names, ips, skipped, err := a.want()
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	cur := a.leafX
	a.mu.Unlock()
	if cur != nil && cur.NotAfter.Sub(a.o.Now()) > renewLeft && slices.Equal(cur.DNSNames, names) && sameIPs(cur.IPAddresses, ips) {
		a.mu.Lock()
		a.skipped = skipped
		a.mu.Unlock()
		return false, nil
	}
	leaf, x, err := a.issue(names, ips)
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	a.leaf, a.leafX, a.skipped = leaf, x, skipped
	a.mu.Unlock()
	return true, nil
}

// want is the leaf's names and addresses as the machine is now: those the
// CA may vouch for.
func (a *Authority) want() (names []string, ips []net.IP, skipped []string, err error) {
	host, err := a.o.Hostname()
	if err != nil {
		return nil, nil, nil, err
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, n := range append([]string{host, host + ".local"}, a.o.Names...) {
		n = strings.ToLower(strings.TrimSuffix(n, "."))
		switch {
		case n == "" || slices.Contains(names, n):
		case a.permitsName(n):
			names = append(names, n)
		default:
			skipped = append(skipped, n)
		}
	}
	addrs, err := a.o.Addrs()
	if err != nil {
		return nil, nil, nil, err
	}
	for _, ad := range append(addrs, netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()) {
		ad = ad.Unmap().WithZone("")
		if !permitted(ad) || slices.ContainsFunc(ips, func(ip net.IP) bool { return ip.Equal(ad.AsSlice()) }) {
			continue
		}
		ips = append(ips, net.IP(ad.AsSlice()))
	}
	slices.SortFunc(ips, func(x, y net.IP) int { return bytes.Compare(x.To16(), y.To16()) })
	return names, ips, skipped, nil
}

func (a *Authority) permitsName(n string) bool {
	for _, d := range a.caCert.PermittedDNSDomains {
		if n == d || strings.HasSuffix(n, "."+d) {
			return true
		}
	}
	return false
}

func permitted(ad netip.Addr) bool {
	return slices.ContainsFunc(Private, func(p netip.Prefix) bool { return p.Contains(ad) })
}

func sameIPs(a, b []net.IP) bool {
	return slices.EqualFunc(a, b, func(x, y net.IP) bool { return x.Equal(y) })
}

func (a *Authority) makeCA() error {
	host, err := a.o.Hostname()
	if err != nil {
		return err
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	domains := []string{host, host + ".local"}
	for _, n := range a.o.Names {
		if n = strings.ToLower(strings.TrimSuffix(n, ".")); n != "" && !slices.Contains(domains, n) {
			domains = append(domains, n)
		}
	}
	var ranges []*net.IPNet
	for _, p := range Private {
		ranges = append(ranges, &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())})
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := a.o.Now()
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{CommonName: "hostd on " + host, Organization: []string{"hostd"}},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(caLife),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         domains,
		PermittedIPRanges:           ranges,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	// The key first: a CA certificate without its key would be useless.
	if err := writeFile(filepath.Join(a.o.Dir, caKey), pemBlock("EC PRIVATE KEY", keyDER), 0o600); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(a.o.Dir, CAFile), pemBlock("CERTIFICATE", der), 0o644); err != nil {
		return err
	}
	return a.loadCA()
}

func (a *Authority) loadCA() error {
	certPEM, err := os.ReadFile(filepath.Join(a.o.Dir, CAFile))
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(filepath.Join(a.o.Dir, caKey))
	if err != nil {
		return err
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return fmt.Errorf("%s: %w", CAFile, err)
	}
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		return fmt.Errorf("%s: no PEM block", caKey)
	}
	key, err := x509.ParseECPrivateKey(b.Bytes)
	if err != nil {
		return fmt.Errorf("%s: %w", caKey, err)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return fmt.Errorf("%s does not belong to %s", caKey, CAFile)
	}
	a.caCert, a.caKey, a.caPEM = cert, key, certPEM
	return nil
}

func (a *Authority) loadLeaf() error {
	pair, err := tls.LoadX509KeyPair(filepath.Join(a.o.Dir, CertFile), filepath.Join(a.o.Dir, KeyFile))
	if err != nil {
		return err
	}
	x, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	// A leaf of another CA (the CA was remade) is not this one's.
	if err := x.CheckSignatureFrom(a.caCert); err != nil {
		return nil //nolint:nilerr // a stale leaf: Refresh issues a new one
	}
	a.leaf, a.leafX = &pair, x
	return nil
}

func (a *Authority) issue(names []string, ips []net.IP) (*tls.Certificate, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := a.o.Now()
	cn := "hostd"
	if len(names) > 0 {
		cn = names[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.caCert, &key.PublicKey, a.caKey)
	if err != nil {
		return nil, nil, err
	}
	x, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	// The chain with the CA, so a client can find the CA it pinned.
	chain := append(pemBlock("CERTIFICATE", der), a.caPEM...)
	if err := writeFile(filepath.Join(a.o.Dir, KeyFile), pemBlock("EC PRIVATE KEY", keyDER), 0o600); err != nil {
		return nil, nil, err
	}
	if err := writeFile(filepath.Join(a.o.Dir, CertFile), chain, 0o644); err != nil {
		return nil, nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, a.caCert.Raw}, PrivateKey: key, Leaf: x}, x, nil
}

// VerifyPinned is a client's tls.Config.VerifyConnection: the server's
// chain must end in the CA with this fingerprint, and its leaf must be
// valid for serving under it. The name the client dialed is not checked:
// a machine is reached by IP, by .local name or over a VPN.
func VerifyPinned(fingerprint string) func(tls.ConnectionState) error {
	fingerprint = strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("the server sent no certificate")
		}
		var ca *x509.Certificate
		for _, c := range cs.PeerCertificates[1:] {
			if Fingerprint(c.Raw) == fingerprint {
				ca = c
			}
		}
		if ca == nil {
			return fmt.Errorf("the server's certificate is not from the CA pinned (%s): another machine, or someone in between", short(fingerprint))
		}
		roots := x509.NewCertPool()
		roots.AddCert(ca)
		_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		return err
	}
}

// ServerCA is the CA a server presents (the last certificate of its
// chain), for a client that has not pinned one yet (hostctl login).
func ServerCA(cs tls.ConnectionState) (*x509.Certificate, error) {
	n := len(cs.PeerCertificates)
	if n < 2 {
		return nil, errors.New("the server sent no CA with its certificate: not a hostd with HTTPS")
	}
	ca := cs.PeerCertificates[n-1]
	if !ca.IsCA {
		return nil, errors.New("the last certificate the server sent is not a CA")
	}
	return ca, nil
}

// Pretty groups a fingerprint for reading aloud: 8 groups of 8.
func Pretty(fp string) string {
	var parts []string
	for i := 0; i < len(fp); i += 8 {
		parts = append(parts, fp[i:min(i+8, len(fp))])
	}
	return strings.Join(parts, " ")
}

func short(fp string) string { return fp[:min(16, len(fp))] + "…" }

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func parseCert(data []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("no certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}

// writeFile writes a file in one step with its mode set before it has
// any content.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func interfaceAddrs() ([]netip.Addr, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range as {
		if n, ok := a.(*net.IPNet); ok {
			if ad, ok := netip.AddrFromSlice(n.IP); ok {
				out = append(out, ad)
			}
		}
	}
	return out, nil
}
