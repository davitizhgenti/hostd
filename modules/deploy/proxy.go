package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// proxy owns a service's public port and sends every request to the live
// release. Switching releases is one atomic swap: requests already on the
// old release finish there (drain), new ones go to the new.
type proxy struct {
	app      string
	srv      *http.Server
	upstream atomic.Pointer[upstream]
	done     chan struct{}
}

type upstream struct {
	port int
	rp   *httputil.ReverseProxy

	mu       sync.Mutex
	active   int           // requests on it
	draining bool          // replaced: takes no new requests
	idle     chan struct{} // closed when draining and no request is left
}

// acquire counts a request on u, unless u is draining.
func (u *upstream) acquire() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.draining {
		return false
	}
	u.active++
	return true
}

func (u *upstream) release() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.active--
	if u.active == 0 && u.idle != nil {
		close(u.idle)
		u.idle = nil
	}
}

// startDrain stops u taking requests; the channel closes once the ones
// on it have finished.
func (u *upstream) startDrain() <-chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.draining = true
	ch := make(chan struct{})
	if u.active == 0 {
		close(ch)
	} else {
		u.idle = ch
	}
	return ch
}

func newUpstream(port int) *upstream {
	u := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "hostd: the service's release is not answering: "+err.Error(), http.StatusBadGateway)
	}
	return &upstream{port: port, rp: rp}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for {
		up := p.upstream.Load()
		if up == nil {
			http.Error(w, "hostd: "+p.app+" has no release deployed yet", http.StatusServiceUnavailable)
			return
		}
		// A replaced upstream is draining: the new one is already in place.
		if up.acquire() {
			defer up.release()
			up.rp.ServeHTTP(w, r)
			return
		}
	}
}

// listen starts serving the public port.
func startProxy(app string, addr string) (*proxy, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%s: public port %s: %w", app, addr, err)
	}
	p := &proxy{app: app, done: make(chan struct{})}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer close(p.done)
		if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = err // the listener went away; Stop reports nothing more
		}
	}()
	return p, nil
}

// switchTo sends new requests to port and returns the previous upstream,
// for draining.
func (p *proxy) switchTo(port int) *upstream {
	return p.upstream.Swap(newUpstream(port))
}

// drain waits until the requests on u finish, at most d.
func drain(u *upstream, d time.Duration) {
	if u == nil {
		return
	}
	select {
	case <-u.startDrain():
	case <-time.After(d):
	}
}

func (p *proxy) stop(ctx context.Context) {
	_ = p.srv.Shutdown(ctx)
	<-p.done
}

// freePort picks a port for a release, on loopback.
func freePort() (int, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
