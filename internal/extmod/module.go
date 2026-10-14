// Package extmod runs external modules: separate programs that speak the
// module protocol (sdk/PROTOCOL.md) over a unix socket, or over the
// program's standard input and output. To the core each is an sdk.Module
// like the built-in ones.
package extmod

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// Protocol is the module protocol version.
const Protocol = 1

// The transports a module can be configured with.
const (
	TransportSocket = "socket"
	TransportStdio  = "stdio"
)

// Options configure one external module.
type Options struct {
	Name    string
	Command []string
	Env     map[string]string
	// Transport is how the program talks to hostd: "socket" (default; it
	// connects to a unix socket) or "stdio" (hostd's side of its standard
	// input and output; its stderr goes to the log).
	Transport string
	// SocketDir holds the modules' sockets (created 0700).
	SocketDir string
	Logger    *slog.Logger
	// ConnectWait is how long the program has to connect (default 10s);
	// Backoff the first wait before a restart (default 1s, doubling to 60s).
	ConnectWait time.Duration
	Backoff     time.Duration
}

// Module is an external module.
type Module struct {
	opts Options
	log  *slog.Logger
	man  sdk.Manifest
	raw  []byte // the manifest as sent, to check a restarted module

	mu      sync.Mutex
	conn    *conn
	proc    *exec.Cmd
	exited  chan struct{} // closed when proc ends
	core    sdk.Core
	handles map[string]context.Context // actions being handled, by ID
	subs    map[string]context.CancelFunc
	nextSub int
	up      bool // connected and started

	life     context.Context
	stopLife context.CancelFunc
	stopping bool
	done     sync.WaitGroup
}

// Load starts the program, does the handshake and reads its manifest. A
// module that fails here is left out; hostd runs without it.
func Load(ctx context.Context, opts Options) (*Module, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ConnectWait == 0 {
		opts.ConnectWait = 10 * time.Second
	}
	if opts.Backoff == 0 {
		opts.Backoff = time.Second
	}
	if len(opts.Command) == 0 {
		return nil, fmt.Errorf("external module %q: no command", opts.Name)
	}
	if opts.Transport == "" {
		opts.Transport = TransportSocket
	}
	if opts.Transport != TransportSocket && opts.Transport != TransportStdio {
		return nil, fmt.Errorf("external module %q: transport %q: use %q or %q", opts.Name, opts.Transport, TransportSocket, TransportStdio)
	}
	life, cancel := context.WithCancel(context.Background())
	m := &Module{opts: opts, log: opts.Logger.With("module", opts.Name), handles: map[string]context.Context{},
		subs: map[string]context.CancelFunc{}, life: life, stopLife: cancel}
	man, raw, err := m.launch(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("external module %q: %w", opts.Name, err)
	}
	m.man, m.raw = man, raw
	return m, nil
}

// launch starts the program and returns its manifest, with the connection
// in place.
func (m *Module) launch(ctx context.Context) (sdk.Manifest, []byte, error) {
	stdio := m.opts.Transport == TransportStdio
	env := append(os.Environ(), "HOSTD_MODULE_NAME="+m.opts.Name, "HOSTD_PROTOCOL="+strconv.Itoa(Protocol),
		"HOSTD_MODULE_TRANSPORT="+m.opts.Transport)
	var ln net.Listener
	var sock string
	if !stdio {
		if err := os.MkdirAll(m.opts.SocketDir, 0o700); err != nil {
			return sdk.Manifest{}, nil, err
		}
		sock = filepath.Join(m.opts.SocketDir, m.opts.Name+".sock")
		_ = os.Remove(sock)
		var lc net.ListenConfig
		var err error
		ln, err = lc.Listen(ctx, "unix", sock)
		if err != nil {
			return sdk.Manifest{}, nil, err
		}
		defer ln.Close()
		_ = os.Chmod(sock, 0o600)
		env = append(env, "HOSTD_MODULE_SOCKET="+sock)
	}
	for k, v := range m.opts.Env {
		env = append(env, k+"="+v)
	}

	cmd := exec.CommandContext(m.life, m.opts.Command[0], m.opts.Command[1:]...) // ends with the module, not with ctx
	cmd.Env = env
	var logs io.Reader // what the program writes for the log
	var wire io.ReadWriteCloser
	if stdio {
		in, err := cmd.StdinPipe()
		if err != nil {
			return sdk.Manifest{}, nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return sdk.Manifest{}, nil, err
		}
		errs, err := cmd.StderrPipe()
		if err != nil {
			return sdk.Manifest{}, nil, err
		}
		logs, wire = errs, &stdioConn{r: out, w: in}
	} else {
		out, err := cmd.StdoutPipe()
		if err != nil {
			return sdk.Manifest{}, nil, err
		}
		cmd.Stderr = cmd.Stdout // one stream into the log
		logs = out
	}
	if err := cmd.Start(); err != nil {
		return sdk.Manifest{}, nil, err
	}
	exited := make(chan struct{})
	go m.logOutput(logs)
	go func() { _ = cmd.Wait(); close(exited) }()
	fail := func(err error) (sdk.Manifest, []byte, error) {
		_ = cmd.Process.Kill()
		<-exited
		return sdk.Manifest{}, nil, err
	}

	c := wire
	if !stdio {
		accepted := make(chan net.Conn, 1)
		go func() {
			if c, err := ln.Accept(); err == nil {
				accepted <- c
			}
		}()
		select {
		case nc := <-accepted:
			c = nc
		case <-exited:
			return fail(errors.New("the program ended before connecting"))
		case <-time.After(m.opts.ConnectWait):
			return fail(fmt.Errorf("the program did not connect to %s within %s", sock, m.opts.ConnectWait))
		case <-ctx.Done():
			return fail(ctx.Err())
		}
	}
	k := newConn(c, m.serve)
	hctx, cancel := context.WithTimeout(ctx, m.opts.ConnectWait)
	defer cancel()
	var hs struct {
		Protocol int `json:"protocol"`
	}
	if err := k.call(hctx, "hostd.handshake", map[string]any{"protocol": Protocol, "core_version": sdk.CoreVersion, "name": m.opts.Name}, &hs); err != nil {
		k.close()
		return fail(fmt.Errorf("handshake: %w", err))
	}
	if hs.Protocol != Protocol {
		k.close()
		return fail(fmt.Errorf("handshake: the module speaks protocol %d, hostd %d", hs.Protocol, Protocol))
	}
	var raw json.RawMessage
	if err := k.call(hctx, "manifest", struct{}{}, &raw); err != nil {
		k.close()
		return fail(fmt.Errorf("manifest: %w", err))
	}
	var man sdk.Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		k.close()
		return fail(fmt.Errorf("manifest: %w", err))
	}
	if man.Name != m.opts.Name {
		k.close()
		return fail(fmt.Errorf("manifest: the module calls itself %q, configured as %q", man.Name, m.opts.Name))
	}
	if err := man.Validate(); err != nil {
		k.close()
		return fail(fmt.Errorf("manifest: %w", err))
	}
	m.mu.Lock()
	m.conn, m.proc, m.exited = k, cmd, exited
	m.mu.Unlock()
	return man, raw, nil
}

func (m *Module) logOutput(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		m.log.Info("external module says", "line", sc.Text())
	}
}

func (m *Module) Manifest() sdk.Manifest { return m.man }

// Start starts the module and watches its process: an exit after this
// restarts it, with backoff.
func (m *Module) Start(ctx context.Context, core sdk.Core) error {
	m.mu.Lock()
	m.core = core
	k := m.conn
	m.mu.Unlock()
	if err := k.call(ctx, "start", struct{}{}, nil); err != nil {
		return fmt.Errorf("external module %q: start: %w", m.opts.Name, err)
	}
	m.mu.Lock()
	m.up = true
	m.mu.Unlock()
	m.done.Add(1)
	go m.supervise()
	return nil
}

// supervise restarts the program whenever it ends, until Stop.
func (m *Module) supervise() {
	defer m.done.Done()
	wait := m.opts.Backoff
	for {
		m.mu.Lock()
		exited := m.exited
		m.mu.Unlock()
		select {
		case <-m.life.Done():
			return
		case <-exited:
		}
		m.mu.Lock()
		if m.stopping {
			m.mu.Unlock()
			return
		}
		m.up = false
		if m.conn != nil {
			m.conn.close()
		}
		for id, cancel := range m.subs {
			cancel()
			delete(m.subs, id)
		}
		m.mu.Unlock()
		m.log.Warn("external module ended; starting it again", "in", wait)
		for {
			select {
			case <-m.life.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, time.Minute)
			_, raw, err := m.launch(m.life)
			if err == nil && !bytes.Equal(raw, m.raw) {
				err = errors.New("its manifest changed; restart hostd to load the new one")
				m.mu.Lock()
				if m.proc != nil {
					_ = m.proc.Process.Kill()
				}
				m.mu.Unlock()
			}
			if err == nil {
				m.mu.Lock()
				k := m.conn
				m.mu.Unlock()
				ctx, cancel := context.WithTimeout(m.life, 30*time.Second)
				err = k.call(ctx, "start", struct{}{}, nil)
				cancel()
			}
			if err != nil {
				m.log.Warn("external module did not come back", "err", err, "next try in", wait)
				continue
			}
			m.mu.Lock()
			m.up = true
			m.mu.Unlock()
			m.log.Info("external module is back")
			wait = m.opts.Backoff
			break
		}
	}
}

func (m *Module) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.stopping = true
	k, exited, proc := m.conn, m.exited, m.proc
	m.mu.Unlock()
	if k != nil {
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = k.call(sctx, "stop", struct{}{}, nil)
		cancel()
		k.close()
	}
	if proc != nil {
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			_ = proc.Process.Kill()
			<-exited
		}
	}
	m.stopLife()
	m.done.Wait()
	m.mu.Lock()
	for _, cancel := range m.subs {
		cancel()
	}
	m.mu.Unlock()
	return nil
}

// current is the live connection, or module_unavailable.
func (m *Module) current() (*conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.up || m.conn == nil {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "external module %q is not running (it is being started again)", m.opts.Name)
	}
	return m.conn, nil
}

func (m *Module) callModule(ctx context.Context, method string, params, out any) error {
	k, err := m.current()
	if err != nil {
		return err
	}
	if err := k.call(ctx, method, params, out); err != nil {
		if errors.Is(err, errClosed) {
			return sdk.Errorf(sdk.CodeModuleUnavailable, "external module %q went away", m.opts.Name)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return sdk.Errorf(sdk.CodeTimeout, "external module %q did not answer %s in time", m.opts.Name, method)
		}
		return err
	}
	return nil
}

func (m *Module) Validate(ctx context.Context, a sdk.Action) error {
	return m.callModule(ctx, "validate", map[string]any{"action": a}, nil)
}

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	m.mu.Lock()
	m.handles[a.ID] = ctx
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.handles, a.ID)
		m.mu.Unlock()
	}()
	var res sdk.Result
	if err := m.callModule(ctx, "handle", map[string]any{"action": a}, &res); err != nil {
		return sdk.Result{}, err
	}
	res.Action = a.ID
	if res.Status == "" {
		res.Status = sdk.StatusApplied
	}
	return res, nil
}

func (m *Module) Read(ctx context.Context, name string, params map[string]string) (any, error) {
	var out json.RawMessage
	if err := m.callModule(ctx, "read", map[string]any{"name": name, "params": params}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Module) State(ctx context.Context) (any, error) {
	var out json.RawMessage
	if err := m.callModule(ctx, "state", struct{}{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Module) Hook(ctx context.Context, name string, req sdk.HookRequest) (any, error) {
	var out json.RawMessage
	err := m.callModule(ctx, "hook", map[string]any{"name": name, "params": req.Params, "header": req.Header,
		"body": base64.StdEncoding.EncodeToString(req.Body)}, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// serve answers the module's calls into the core.
func (m *Module) serve(method string, params json.RawMessage) (any, error) {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		return nil, sdk.Errorf(sdk.CodeModuleUnavailable, "%s before start", method)
	}
	bad := func(err error) error { return sdk.Errorf(sdk.CodeInvalidArgs, "%s: %v", method, err) }
	switch method {
	case "emit":
		var p struct {
			Event sdk.Event `json:"event"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, bad(err)
		}
		core.Emit(p.Event)
		return struct{}{}, nil
	case "do":
		var p struct {
			Action sdk.Action `json:"action"`
			Within string     `json:"within"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, bad(err)
		}
		ctx := m.life
		if p.Within != "" {
			m.mu.Lock()
			hctx, ok := m.handles[p.Within]
			m.mu.Unlock()
			if !ok {
				return nil, sdk.Errorf(sdk.CodeInvalidArgs, "do: action %q is not being handled by this module", p.Within)
			}
			ctx = hctx
		}
		a := sdk.Action{Type: p.Action.Type, Args: p.Action.Args, ExpectVersion: p.Action.ExpectVersion, Cause: p.Action.Cause}
		return core.Do(ctx, a)
	case "subscribe":
		var p struct {
			Filter string `json:"filter"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Filter == "" {
			return nil, bad(errors.New("needs a filter"))
		}
		ctx, cancel := context.WithCancel(m.life)
		m.mu.Lock()
		m.nextSub++
		id := "s" + strconv.Itoa(m.nextSub)
		m.subs[id] = cancel
		k := m.conn
		m.mu.Unlock()
		events := core.Subscribe(ctx, p.Filter)
		go func() {
			for ev := range events {
				if err := k.notify("event", map[string]any{"subscription": id, "event": ev}); err != nil {
					cancel()
				}
			}
		}()
		return map[string]string{"subscription": id}, nil
	case "unsubscribe":
		var p struct {
			Subscription string `json:"subscription"`
		}
		_ = json.Unmarshal(params, &p)
		m.mu.Lock()
		if cancel := m.subs[p.Subscription]; cancel != nil {
			cancel()
			delete(m.subs, p.Subscription)
		}
		m.mu.Unlock()
		return struct{}{}, nil
	case "read":
		var p struct {
			Module string            `json:"module"`
			Read   string            `json:"read"`
			Params map[string]string `json:"params"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, bad(err)
		}
		return core.Read(m.life, p.Module, p.Read, p.Params)
	case "handles":
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(params, &p)
		return map[string]bool{"handles": core.Handles(p.Type)}, nil
	}
	return nil, sdk.Errorf(sdk.CodeNotFound, "no method %q", method)
}

// stdioConn is hostd's end of a program's standard input and output. Closing
// it closes the program's input: a module that reads until end of file ends
// by itself.
type stdioConn struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *stdioConn) Close() error {
	werr := c.w.Close()
	_ = c.r.Close()
	return werr
}
