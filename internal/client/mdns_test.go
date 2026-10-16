package client

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// responder answers mDNS questions on loopback as a machine called core
// does, with a compressed name and another record first.
func responder(t *testing.T) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	old := mdnsGroup
	mdnsGroup = conn.LocalAddr().(*net.UDPAddr)
	t.Cleanup(func() { mdnsGroup = old })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			name, _, _ := readName(q, 12)
			if name != "core.local" {
				continue // nobody else answers
			}
			// Header: the question's ID, a response, one question, two answers.
			r := append([]byte{}, q[:2]...)
			r = append(r, 0x84, 0, 0, 1, 0, 2, 0, 0, 0, 0)
			r = append(r, q[12:]...) // the question (name at offset 12)
			// A TXT record for the name, then the A record, both by pointer.
			r = append(r, 0xc0, 12, 0, 16, 0x80, 1, 0, 0, 0, 120, 0, 1, 0)
			r = append(r, 0xc0, 12, 0, 1, 0x80, 1, 0, 0, 0, 120, 0, 4, 192, 168, 18, 33)
			_, _ = conn.WriteToUDP(r, from)
		}
	}()
}

func TestResolveLocal(t *testing.T) {
	responder(t)
	old := mdnsWait
	mdnsWait = 200 * time.Millisecond
	t.Cleanup(func() { mdnsWait = old })
	ctx := context.Background()
	for in, want := range map[string]string{
		"core.local:7300":    "192.168.18.33:7300",
		"Core.Local.:7300":   "192.168.18.33:7300",
		"10.0.0.1:7300":      "10.0.0.1:7300",
		"core:7300":          "core:7300",
		"example.org:443":    "example.org:443",
		"no-port.local":      "no-port.local",
		"nobody.local:7300":  "nobody.local:7300", // no answer: left to the system
		"bad..name.local:73": "bad..name.local:73",
	} {
		if got := ResolveLocal(ctx, in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestMDNSAnswerIgnoresOthers(t *testing.T) {
	q, id, err := mdnsQuestion("core.local")
	if err != nil {
		t.Fatal(err)
	}
	reply := func(id uint16, flags byte, name string) []byte {
		r := binary.BigEndian.AppendUint16(nil, id)
		r = append(r, flags, 0, 0, 0, 0, 1, 0, 0, 0, 0)
		for _, l := range []string{name, "local"} {
			r = append(r, byte(len(l)))
			r = append(r, l...)
		}
		return append(r, 0, 0, 1, 0, 1, 0, 0, 0, 120, 0, 4, 10, 0, 0, 9)
	}
	if ip := mdnsAnswer(reply(id, 0x84, "core"), id, "core.local"); !ip.Equal(net.IPv4(10, 0, 0, 9)) {
		t.Fatalf("answer: %v", ip)
	}
	for name, r := range map[string][]byte{
		"another ID":   reply(id+1, 0x84, "core"),
		"a question":   reply(id, 0x04, "core"),
		"another name": reply(id, 0x84, "other"),
		"cut short":    reply(id, 0x84, "core")[:30],
		"too short":    q[:5],
	} {
		if ip := mdnsAnswer(r, id, "core.local"); ip != nil {
			t.Errorf("%s: %v", name, ip)
		}
	}
	// A pointer loop ends.
	if _, _, ok := readName([]byte{0xc0, 0}, 0); ok {
		t.Fatal("a pointer to itself was read")
	}
}
