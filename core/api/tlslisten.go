package api

import (
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// splitTLS shares one TCP listener between HTTPS and plain HTTP: a
// connection whose first byte starts a TLS handshake (0x16) goes to the
// first listener, as a TLS connection; anything else to the second. The
// API is served over the first. The second only answers with the public
// docs and a pointer to https://, so a token sent there is never used.
func splitTLS(ln net.Listener, cfg *tls.Config) (secure, plain net.Listener) {
	sp := &splitter{ln: ln, cfg: cfg, secure: make(chan net.Conn), plain: make(chan net.Conn), done: make(chan struct{})}
	go sp.run()
	return &splitHalf{sp, sp.secure}, &splitHalf{sp, sp.plain}
}

type splitter struct {
	ln            net.Listener
	cfg           *tls.Config
	secure, plain chan net.Conn
	done          chan struct{}
	closeOnce     sync.Once
}

func (sp *splitter) run() {
	defer sp.close()
	for {
		c, err := sp.ln.Accept()
		if err != nil {
			return
		}
		go sp.classify(c)
	}
}

// classify reads a connection's first byte (giving it 10 seconds, as the
// servers give for headers) and hands it on with that byte put back.
func (sp *splitter) classify(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	first := make([]byte, 1)
	if _, err := c.Read(first); err != nil {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	pc := &peekedConn{Conn: c, first: first}
	var out net.Conn = pc
	ch := sp.plain
	if first[0] == 0x16 {
		out, ch = tls.Server(pc, sp.cfg), sp.secure
	}
	select {
	case ch <- out:
	case <-sp.done:
		c.Close()
	}
}

func (sp *splitter) close() {
	sp.closeOnce.Do(func() {
		close(sp.done)
		sp.ln.Close()
	})
}

type splitHalf struct {
	sp *splitter
	ch chan net.Conn
}

func (h *splitHalf) Accept() (net.Conn, error) {
	select {
	case c := <-h.ch:
		return c, nil
	case <-h.sp.done:
		return nil, net.ErrClosed
	}
}

func (h *splitHalf) Close() error   { h.sp.close(); return nil }
func (h *splitHalf) Addr() net.Addr { return h.sp.ln.Addr() }

// peekedConn gives back the byte classify read before the rest.
type peekedConn struct {
	net.Conn
	first []byte
}

func (c *peekedConn) Read(p []byte) (int, error) {
	if len(c.first) > 0 {
		n := copy(p, c.first)
		c.first = c.first[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// plainHandler is what plain HTTP on the TLS port gets: the docs (public
// pages with no secrets, readable before a browser trusts the CA), and
// otherwise an error that says where the API is.
func (s *Server) plainHandler() http.Handler {
	mux := http.NewServeMux()
	if s.opts.DocsDir != "" {
		mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.FileServer(http.Dir(s.opts.DocsDir))))
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/docs/", http.StatusFound) })
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		writeError(w, r, http.StatusBadRequest, sdk.CodeInvalidArgs,
			"this port speaks HTTPS: use https://"+r.Host+r.URL.Path+" (hostctl login "+r.Host+"). A token sent here over plain HTTP was not used, but it crossed the network unencrypted: consider revoking it")
	})
	return mux
}
