package client

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"net"
	"strings"
	"time"
)

// .local names (core.local) are resolved with one mDNS question, by
// hostctl itself: a build without cgo has no mDNS (Go's resolver asks the
// DNS server, which does not know them), and with cgo some systems take
// seconds over it (nss-mdns after other sources in nsswitch.conf). The
// machine answers within milliseconds; without an answer the system's
// resolver is used as before.

// mdnsGroup is where questions go; tests point it at their own responder.
var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

var mdnsWait = 1500 * time.Millisecond // tests shorten it

// ResolveLocal returns hostport with a .local host replaced by its IPv4
// address, if a machine answers for it; anything else unchanged.
func ResolveLocal(ctx context.Context, hostport string) string {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || !strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".local") {
		return hostport
	}
	ip, err := lookupMDNS(ctx, host)
	if err != nil {
		return hostport
	}
	return net.JoinHostPort(ip.String(), port)
}

// dial is the transport's dialer: .local names through mDNS.
func dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, ResolveLocal(ctx, addr))
}

// lookupMDNS asks for host's IPv4 address as a one-shot ("legacy
// unicast", RFC 6762 6.7) query: from a port other than 5353, so the
// answer comes back to it, as a reply to this question.
func lookupMDNS(ctx context.Context, host string) (net.IP, error) {
	q, id, err := mdnsQuestion(host)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(mdnsWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.WriteToUDP(q, mdnsGroup); err != nil {
		return nil, err
	}
	buf := make([]byte, 9000)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil, err // the deadline: nobody answered
		}
		if ip := mdnsAnswer(buf[:n], id, host); ip != nil {
			return ip, nil
		}
	}
}

// mdnsQuestion is a DNS query for host's A record, with a random ID.
func mdnsQuestion(host string) ([]byte, uint16, error) {
	id := uint16(rand.Uint32()) //nolint:gosec // matches the reply, not a secret
	b := binary.BigEndian.AppendUint16(nil, id)
	b = append(b, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0) // flags; one question
	for _, l := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if l == "" || len(l) > 63 {
			return nil, 0, errors.New("not a host name")
		}
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0, 0, 1, 0, 1), id, nil // type A, class IN
}

// mdnsAnswer reads the IPv4 address for host from a reply to question id,
// or nil.
func mdnsAnswer(b []byte, id uint16, host string) net.IP {
	if len(b) < 12 || binary.BigEndian.Uint16(b) != id || b[2]&0x80 == 0 {
		return nil
	}
	qd, an := int(binary.BigEndian.Uint16(b[4:])), int(binary.BigEndian.Uint16(b[6:]))
	off := 12
	for range qd {
		_, next, ok := readName(b, off)
		if !ok || next+4 > len(b) {
			return nil
		}
		off = next + 4
	}
	want := strings.ToLower(strings.TrimSuffix(host, "."))
	for range an {
		name, next, ok := readName(b, off)
		if !ok || next+10 > len(b) {
			return nil
		}
		typ, class := binary.BigEndian.Uint16(b[next:]), binary.BigEndian.Uint16(b[next+2:])&0x7fff
		size := int(binary.BigEndian.Uint16(b[next+8:]))
		data := next + 10
		if data+size > len(b) {
			return nil
		}
		if typ == 1 && class == 1 && size == 4 && strings.EqualFold(name, want) {
			return net.IPv4(b[data], b[data+1], b[data+2], b[data+3])
		}
		off = data + size
	}
	return nil
}

// readName reads a possibly compressed name at off: the name, and where
// what follows it starts.
func readName(b []byte, off int) (string, int, bool) {
	var labels []string
	next := -1
	for jumps := 0; jumps < 16; {
		if off >= len(b) {
			return "", 0, false
		}
		l := int(b[off])
		switch {
		case l == 0:
			if next < 0 {
				next = off + 1
			}
			return strings.Join(labels, "."), next, true
		case l&0xc0 == 0xc0:
			if off+1 >= len(b) {
				return "", 0, false
			}
			if next < 0 {
				next = off + 2
			}
			off = int(binary.BigEndian.Uint16(b[off:]) & 0x3fff)
			jumps++
		default:
			if off+1+l > len(b) {
				return "", 0, false
			}
			labels = append(labels, strings.ToLower(string(b[off+1:off+1+l])))
			off += 1 + l
		}
	}
	return "", 0, false
}
