package extmod

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/davitizhgenti/hostd/sdk"
)

// conn is one JSON-RPC 2.0 connection with requests in both directions,
// one message per line (sdk/PROTOCOL.md).
type conn struct {
	c io.ReadWriteCloser

	wmu sync.Mutex // one message at a time
	w   *bufio.Writer

	mu      sync.Mutex
	next    int
	pending map[string]chan message
	closed  bool
	done    chan struct{}

	// handle answers the module's requests (nil result for a notification).
	handle func(method string, params json.RawMessage) (any, error)
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		Code sdk.Code `json:"code"`
	} `json:"data,omitempty"`
}

// errClosed: the module's connection is gone.
var errClosed = errors.New("the module's connection closed")

func newConn(c io.ReadWriteCloser, handle func(string, json.RawMessage) (any, error)) *conn {
	k := &conn{c: c, w: bufio.NewWriter(c), pending: map[string]chan message{}, done: make(chan struct{}), handle: handle}
	go k.readLoop()
	return k
}

func (k *conn) send(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	k.wmu.Lock()
	defer k.wmu.Unlock()
	if _, err := k.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return k.w.Flush()
}

// call sends a request and waits for its answer, or ctx.
func (k *conn) call(ctx context.Context, method string, params, out any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return errClosed
	}
	k.next++
	id := strconv.Itoa(k.next)
	ch := make(chan message, 1)
	k.pending[id] = ch
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		delete(k.pending, id)
		k.mu.Unlock()
	}()
	if err := k.send(message{ID: json.RawMessage(strconv.Quote(id)), Method: method, Params: p}); err != nil {
		return fmt.Errorf("%w: %w", errClosed, err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-k.done:
		return errClosed
	case m := <-ch:
		if m.Error != nil {
			code := sdk.CodeInternal
			if m.Error.Data != nil && m.Error.Data.Code != "" {
				code = m.Error.Data.Code
			}
			return &sdk.Error{Code: code, Message: m.Error.Message}
		}
		if out != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("%s: the module's answer: %w", method, err)
			}
		}
		return nil
	}
}

// notify sends a notification (no answer).
func (k *conn) notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return k.send(message{Method: method, Params: p})
}

func (k *conn) readLoop() {
	defer k.close()
	r := bufio.NewReaderSize(k.c, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var m message
			if json.Unmarshal(line, &m) == nil {
				k.dispatch(m)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = err
			}
			return
		}
	}
}

func (k *conn) dispatch(m message) {
	if m.Method == "" { // an answer
		id, err := strconv.Unquote(string(m.ID))
		if err != nil {
			id = string(m.ID)
		}
		k.mu.Lock()
		ch := k.pending[id]
		k.mu.Unlock()
		if ch != nil {
			ch <- m
		}
		return
	}
	go func() { // a request: may take a while (do), others go on
		res, err := k.handle(m.Method, m.Params)
		if len(m.ID) == 0 {
			return // a notification
		}
		reply := message{ID: m.ID}
		if err != nil {
			se := sdk.AsError(err)
			reply.Error = &rpcError{Code: -32000, Message: se.Message, Data: &struct {
				Code sdk.Code `json:"code"`
			}{se.Code}}
		} else {
			b, merr := json.Marshal(res)
			if merr != nil {
				b = []byte("{}")
			}
			reply.Result = b
		}
		_ = k.send(reply)
	}()
}

func (k *conn) close() {
	k.mu.Lock()
	if !k.closed {
		k.closed = true
		close(k.done)
	}
	k.mu.Unlock()
	_ = k.c.Close()
}
