package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/sdk"
)

// WebSocket subprotocols. Browsers cannot set headers on WebSocket
// connections, so a token can also travel as a subprotocol: the client
// offers both "hostd.v1" and "hostd.token.<secret>", and the server answers
// with "hostd.v1" only, never echoing the token. Subprotocols, unlike query
// parameters, do not end up in access logs.
const (
	wsProtocol    = "hostd.v1"
	wsTokenPrefix = "hostd.token."
	writeTimeout  = 10 * time.Second
)

func websocketProtocols(r *http.Request) []string {
	var out []string
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// sinceStart asks for every event still in the replay window.
const sinceStart = "start"

// getEvents streams events as JSON text messages. ?type= filters by type
// pattern (default "*"); ?since=<event id> first replays what the client
// missed, from the last ReplayEvents events, and ?since=start replays all
// of them. A client that reads too slowly
// gets bus.lagged and the connection is closed; it can reconnect with since.
func (s *Server) getEvents(w http.ResponseWriter, r *http.Request, _ store.Token) error {
	filter := r.URL.Query().Get("type")
	if filter == "" {
		filter = "*"
	}
	since := r.URL.Query().Get("since")

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{wsProtocol},
		// Every connection needs a token, which a foreign web page cannot
		// know, so origin checks would add nothing.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil // Accept already wrote the HTTP error
	}
	defer func() { _ = conn.CloseNow() }()
	ctx := conn.CloseRead(r.Context()) // we only send; this notices the client leaving

	// Subscribe before reading the replay window, so nothing falls between.
	live := s.engine.Subscribe(ctx, filter)
	last := ""
	if since != "" {
		missed, found := s.ring.since(since)
		if since == sinceStart { // the whole replay window, oldest first
			found = true
		}
		if !found {
			if err := send(ctx, conn, sdk.Event{Type: "bus.gap", Time: time.Now().UTC(),
				Data: json.RawMessage(`{"reason":"since is older than the replay window"}`)}); err != nil {
				return nil
			}
		}
		for _, ev := range missed {
			if !sdk.MatchType(filter, ev.Type) {
				continue
			}
			if err := send(ctx, conn, ev); err != nil {
				return nil
			}
			last = ev.ID
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-live:
			if !ok {
				conn.Close(websocket.StatusTryAgainLater, "event stream closed")
				return nil
			}
			if last != "" && ev.ID != "" && ev.ID <= last { // already sent from the replay
				continue
			}
			if err := send(ctx, conn, ev); err != nil {
				return nil
			}
			if ev.Type == core.EventLagged {
				conn.Close(websocket.StatusTryAgainLater, "client too slow; reconnect with since")
				return nil
			}
		}
	}
}

func send(ctx context.Context, conn *websocket.Conn, ev sdk.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}
