// Package client talks to the hostd API. hostctl uses it, and so will the
// on-screen menu and anything else written in Go.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/davitizhgenti/hostd/sdk"
)

// Client is a hostd API client.
type Client struct {
	base   string // http://host:port, or http://hostd for unix sockets
	wsBase string
	token  string
	http   *http.Client
	dialer func(ctx context.Context) (net.Conn, error) // unix sockets only
}

// New returns a client for addr, which is "unix:///path/to/hostd.sock",
// "http://host:7300", or a bare "host:7300".
func New(addr, token string) (*Client, error) {
	c := &Client{token: token}
	switch {
	case strings.HasPrefix(addr, "unix://"):
		path := strings.TrimPrefix(addr, "unix://")
		if path == "" {
			return nil, fmt.Errorf("unix address %q has no path", addr)
		}
		c.dialer = func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}
		c.base, c.wsBase = "http://hostd", "ws://hostd"
		c.http = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return c.dialer(ctx) },
		}}
	default:
		if !strings.Contains(addr, "://") {
			addr = "http://" + addr
		}
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("address %q: use unix:///path, http://host:port or host:port", addr)
		}
		c.base = strings.TrimSuffix(u.Scheme+"://"+u.Host, "/")
		c.wsBase = "ws" + strings.TrimPrefix(c.base, "http")
		c.http = &http.Client{}
	}
	return c, nil
}

// Error is returned for responses with an error body. It wraps the
// sdk.Error, so sdk.CodeOf works on it.
type Error struct {
	Status int
	Err    *sdk.Error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Do sends a request and decodes a JSON response into out (if not nil).
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach hostd: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		var eb struct {
			Error *sdk.Error `json:"error"`
		}
		if json.Unmarshal(b, &eb) == nil && eb.Error != nil {
			return &Error{Status: res.StatusCode, Err: eb.Error}
		}
		return &Error{Status: res.StatusCode, Err: &sdk.Error{Code: sdk.CodeInternal,
			Message: fmt.Sprintf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(b)))}}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// Version is GET /v1/version.
type Version struct {
	Version        string   `json:"version"`
	Dev            bool     `json:"dev"`
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	Schema         int      `json:"schema"`
	Previous       string   `json:"previous,omitempty"`
	RolledBackFrom string   `json:"rolled_back_from,omitempty"`
	Installed      []string `json:"installed,omitempty"`
}

// UpdateResult is the answer of POST /v1/update.
type UpdateResult struct {
	Version    string `json:"version"`
	Previous   string `json:"previous"`
	Restarting bool   `json:"restarting"`
}

// Update uploads a hostd binary; the server installs it and restarts.
func (c *Client) Update(ctx context.Context, binary io.Reader) (UpdateResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/update", binary)
	if err != nil {
		return UpdateResult{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("cannot reach hostd: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		var eb struct {
			Error *sdk.Error `json:"error"`
		}
		if json.Unmarshal(b, &eb) == nil && eb.Error != nil {
			return UpdateResult{}, &Error{Status: res.StatusCode, Err: eb.Error}
		}
		return UpdateResult{}, fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var r UpdateResult
	return r, json.Unmarshal(b, &r)
}

func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	return v, c.Do(ctx, http.MethodGet, "/v1/version", nil, &v)
}

// Get reads any GET route into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) Manifests(ctx context.Context) ([]sdk.Manifest, error) {
	var m []sdk.Manifest
	return m, c.Do(ctx, http.MethodGet, "/v1/manifests", nil, &m)
}

// ActionRequest is the body of POST /v1/actions.
type ActionRequest struct {
	Type          string          `json:"type"`
	Args          json.RawMessage `json:"args,omitempty"`
	ExpectVersion *uint64         `json:"expect_version,omitempty"`
	Cause         string          `json:"cause,omitempty"`
}

// Submit sends an action. Skipped actions are results, not errors.
func (c *Client) Submit(ctx context.Context, req ActionRequest) (sdk.Result, error) {
	var r sdk.Result
	return r, c.Do(ctx, http.MethodPost, "/v1/actions", req, &r)
}

// AuditRecord is one entry of GET /v1/actions.
type AuditRecord struct {
	Seq      int64           `json:"seq"`
	Time     time.Time       `json:"time"`
	Action   string          `json:"action"`
	Type     string          `json:"type"`
	Args     json.RawMessage `json:"args"`
	Source   sdk.Source      `json:"source"`
	Status   string          `json:"status"`
	Code     sdk.Code        `json:"code"`
	Reason   string          `json:"reason"`
	HeldBy   sdk.SourceKind  `json:"held_by"`
	Until    *time.Time      `json:"until"`
	Resource string          `json:"resource"`
	Version  uint64          `json:"version"`
}

func (c *Client) Audit(ctx context.Context, query url.Values) ([]AuditRecord, error) {
	var recs []AuditRecord
	path := "/v1/actions"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return recs, c.Do(ctx, http.MethodGet, path, nil, &recs)
}

// Token is a token as the API lists it; Secret is only set on creation.
type Token struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Kind     sdk.SourceKind `json:"kind"`
	Scopes   []string       `json:"scopes"`
	Created  time.Time      `json:"created"`
	Expires  *time.Time     `json:"expires,omitempty"`
	Revoked  *time.Time     `json:"revoked,omitempty"`
	LastUsed *time.Time     `json:"last_used,omitempty"`
	Secret   string         `json:"secret,omitempty"`
}

// TokenRequest is the body of POST /v1/tokens.
type TokenRequest struct {
	Name   string         `json:"name"`
	Kind   sdk.SourceKind `json:"kind,omitempty"`
	Scopes []string       `json:"scopes"`
	TTL    string         `json:"ttl,omitempty"`
}

func (c *Client) CreateToken(ctx context.Context, req TokenRequest) (Token, error) {
	var t Token
	return t, c.Do(ctx, http.MethodPost, "/v1/tokens", req, &t)
}

func (c *Client) Tokens(ctx context.Context) ([]Token, error) {
	var t []Token
	return t, c.Do(ctx, http.MethodGet, "/v1/tokens", nil, &t)
}

func (c *Client) RevokeToken(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/tokens/"+url.PathEscape(id), nil, nil)
}

// ErrStop can be returned by an Events callback to end the stream cleanly.
var ErrStop = errors.New("stop events")

// Events streams events matching filter to fn until ctx ends or fn returns
// an error (ErrStop ends it without one). After a dropped connection it
// reconnects on its own, asking for what it missed (since), and waits
// longer between each failed attempt. Authentication and permission errors
// end it at once.
//
// since is where to start: "" for new events only, "start" for every event
// the server still keeps (its last 1,000), or an event ID to continue after.
func (c *Client) Events(ctx context.Context, filter, since string, fn func(sdk.Event) error) error {
	backoff := 200 * time.Millisecond
	for {
		var fnErr error
		err := c.streamOnce(ctx, filter, since, func(ev sdk.Event) bool {
			backoff = 200 * time.Millisecond
			if ev.ID != "" {
				since = ev.ID
			}
			fnErr = fn(ev)
			return fnErr == nil
		})
		if fnErr != nil {
			if errors.Is(fnErr, ErrStop) {
				return nil
			}
			return fnErr
		}
		select {
		case <-ctx.Done(): // cancelled: a normal end, not an error
			return nil
		default:
		}
		var ce *Error
		if errors.As(err, &ce) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// streamOnce reads one connection until it drops, ctx ends, or fn returns
// false.
func (c *Client) streamOnce(ctx context.Context, filter, since string, fn func(sdk.Event) bool) error {
	q := url.Values{}
	if filter != "" {
		q.Set("type", filter)
	}
	if since != "" {
		q.Set("since", since)
	}
	opts := &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{}}
	if c.token != "" {
		opts.HTTPHeader.Set("Authorization", "Bearer "+c.token)
	}
	conn, res, err := websocket.Dial(ctx, c.wsBase+"/v1/events?"+q.Encode(), opts)
	if res != nil && res.Body != nil {
		defer res.Body.Close()
	}
	if err != nil {
		if res != nil && res.StatusCode >= 400 {
			var eb struct {
				Error *sdk.Error `json:"error"`
			}
			b, _ := io.ReadAll(res.Body)
			if json.Unmarshal(b, &eb) == nil && eb.Error != nil {
				return &Error{Status: res.StatusCode, Err: eb.Error}
			}
		}
		return err
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(16 << 20)
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var ev sdk.Event
		if err := json.Unmarshal(b, &ev); err != nil {
			return err
		}
		if !fn(ev) {
			return nil
		}
	}
}
