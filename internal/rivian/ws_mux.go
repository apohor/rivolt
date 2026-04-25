package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// wsMux owns a single shared WS connection to Rivian's subscription
// gateway and demultiplexes incoming frames by subscription id.
// Multiple concurrent Subscribe* calls on one LiveClient share the
// single connection — Rivian's gateway rejects concurrent connections
// from the same session token, so multiplexing is required for any
// scenario with more than one active subscription (e.g. VehicleState
// + ChargingSession + Parallax running in parallel during charging).
//
// Lifecycle: the mux is created on demand by LiveClient.acquireMux
// and torn down when the last subscription releases it (ref count
// hits zero) or when the connection fails. Failure kicks every
// active subscription with errCh so their outer retry loops
// reconnect, which produces a new mux.
type wsMux struct {
	// Immutable after construction.
	readyCh chan struct{} // closed once connection_ack is received.
	doneCh  chan struct{} // closed when the receiver goroutine exits

	// Write side — serialise all writes to the conn via writeMu.
	writeMu sync.Mutex
	conn    *websocket.Conn

	// Mutable state.
	mu       sync.Mutex
	subs     map[string]*wsSub // id → active sub
	refs     int               // number of live subscribe() holders (drives close)
	shutdown bool              // true once close() has been called
	err      error             // terminal error that killed the mux
}

// wsSub is a registered subscription. The mux fans incoming "next"
// payloads to framesCh and terminal error/complete signals to errCh.
// errCh receives at most one value, then is closed.
type wsSub struct {
	id       string
	framesCh chan json.RawMessage
	errCh    chan error
}

// acquireMux returns a connected mux ready to subscribe on. If one
// already exists and is healthy, it's reused with refs++. Otherwise
// a new connection is dialled, connection_init is sent, and the
// caller blocks until connection_ack arrives or auth/dial fails.
//
// Each successful acquireMux MUST be paired with a releaseMux, which
// decrements refs and closes the WS when the last holder leaves.
func (c *LiveClient) acquireMux(ctx context.Context) (*wsMux, error) {
	if err := c.checkUpstream(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	userTok := c.userSessionToken
	c.mu.Unlock()
	if userTok == "" {
		return nil, errors.New("rivian: not authenticated; call Login first")
	}

	c.muxMu.Lock()
	// Reuse existing mux if it hasn't been torn down. A mux that
	// hit a terminal error will have shutdown=true; we discard it
	// and dial a fresh one.
	if c.mux != nil {
		c.mux.mu.Lock()
		if !c.mux.shutdown && c.mux.err == nil {
			c.mux.refs++
			c.mux.mu.Unlock()
			mux := c.mux
			c.muxMu.Unlock()
			// Wait for ack if we hit a mux whose ack hasn't landed
			// yet (another goroutine is racing with us).
			if err := mux.waitReady(ctx); err != nil {
				c.releaseMux(mux)
				return nil, err
			}
			return mux, nil
		}
		c.mux.mu.Unlock()
		c.mux = nil
	}

	// Dial a fresh connection. Holding muxMu across the dial means
	// concurrent acquireMux callers queue behind us — simpler than
	// a "connecting" phase and the dial is fast on success.
	mux := &wsMux{
		readyCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
		subs:    map[string]*wsSub{},
		refs:    1,
	}
	conn, _, err := websocket.Dial(ctx, wsEndpoint, &websocket.DialOptions{
		Subprotocols: []string{"graphql-transport-ws"},
	})
	if err != nil {
		c.muxMu.Unlock()
		return nil, fmt.Errorf("ws dial: %w", err)
	}
	conn.SetReadLimit(1 << 20)
	mux.conn = conn

	initFrame := map[string]any{
		"type": "connection_init",
		"payload": map[string]any{
			"client-name":    c.clientName,
			"client-version": apolloClientVersion,
			"dc-cid":         "m-ios-" + uuid.NewString(),
			"u-sess":         userTok,
		},
	}
	if err := mux.writeJSON(ctx, initFrame); err != nil {
		conn.Close(websocket.StatusInternalError, "init failed") //nolint:errcheck
		c.muxMu.Unlock()
		return nil, fmt.Errorf("ws init: %w", err)
	}

	// Receiver dispatches all frames for this connection. It runs
	// until the connection dies or the mux is closed.
	go mux.receive()

	c.mux = mux
	c.muxMu.Unlock()

	if err := mux.waitReady(ctx); err != nil {
		c.releaseMux(mux)
		return nil, err
	}
	return mux, nil
}

// releaseMux decrements the ref count and tears down the connection
// when the last holder leaves. Safe to call on a mux that's already
// failed — close() is idempotent.
func (c *LiveClient) releaseMux(m *wsMux) {
	c.muxMu.Lock()
	m.mu.Lock()
	m.refs--
	shouldClose := m.refs <= 0
	m.mu.Unlock()
	if shouldClose {
		m.close(nil)
		if c.mux == m {
			c.mux = nil
		}
	}
	c.muxMu.Unlock()
}

// waitReady blocks until connection_ack is received or the mux fails.
// Returns nil on ack, an error on dial-side failure or ctx.Done.
// Safe to call from multiple goroutines — readyCh is only closed once.
func (m *wsMux) waitReady(ctx context.Context) error {
	select {
	case <-m.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.doneCh:
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.err != nil {
			return m.err
		}
		return errors.New("ws mux closed before ack")
	case <-time.After(10 * time.Second):
		return errors.New("ws await ack: timeout")
	}
}

// subscribe registers a new subscription on the mux and sends the
// GraphQL "subscribe" frame. Returns the wsSub (caller reads from
// framesCh / errCh) and an unsubscribe func that MUST be called
// exactly once — typically via defer. The unsubscribe sends a
// "complete" frame and removes the sub from the dispatch table.
func (m *wsMux) subscribe(ctx context.Context, operationName, query string, variables map[string]any) (*wsSub, func(), error) {
	m.mu.Lock()
	if m.shutdown {
		err := m.err
		m.mu.Unlock()
		if err == nil {
			err = errors.New("ws mux: closed")
		}
		return nil, nil, err
	}
	sub := &wsSub{
		id:       uuid.NewString(),
		framesCh: make(chan json.RawMessage, 16),
		errCh:    make(chan error, 1),
	}
	m.subs[sub.id] = sub
	m.mu.Unlock()

	payload, _ := json.Marshal(map[string]any{
		"operationName": operationName,
		"query":         query,
		"variables":     variables,
	})
	frame := map[string]any{
		"id":      sub.id,
		"type":    "subscribe",
		"payload": json.RawMessage(payload),
	}
	if err := m.writeJSON(ctx, frame); err != nil {
		m.mu.Lock()
		delete(m.subs, sub.id)
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("ws subscribe: %w", err)
	}

	unsubscribe := func() {
		m.mu.Lock()
		if _, ok := m.subs[sub.id]; !ok {
			m.mu.Unlock()
			return
		}
		delete(m.subs, sub.id)
		shutdown := m.shutdown
		m.mu.Unlock()
		if shutdown {
			return
		}
		// Best-effort "complete" frame — if the write fails the
		// server will clean up when the connection eventually drops.
		complete := map[string]any{"id": sub.id, "type": "complete"}
		writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = m.writeJSON(writeCtx, complete)
		cancel()
	}
	return sub, unsubscribe, nil
}

// writeJSON serialises v and writes one text frame. Serialises across
// concurrent callers via writeMu — coder/websocket panics on
// concurrent writes.
func (m *wsMux) writeJSON(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if m.conn == nil {
		return errors.New("ws mux: no connection")
	}
	return m.conn.Write(ctx, websocket.MessageText, data)
}

// receive is the mux's single reader goroutine. It decodes every
// incoming frame and dispatches to the matching wsSub by id.
// Terminal errors (connection_error, read failures) close the mux
// and deliver the error to every active sub.
func (m *wsMux) receive() {
	defer close(m.doneCh)

	ackDelivered := false
	markReady := func() {
		if ackDelivered {
			return
		}
		ackDelivered = true
		close(m.readyCh)
	}

	for {
		// Per-frame read timeout is generous — Rivian's gateway ka's
		// aren't super regular and an idle VehicleState subscription
		// can go minutes between frames. If the server genuinely
		// stops responding, the outer TCP keepalive eventually kills
		// the conn and we'll break out here.
		readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		_, data, err := m.conn.Read(readCtx)
		cancel()
		if err != nil {
			// Before ack, a read error ends up as the mux's terminal
			// err; waitReady picks it up via doneCh and surfaces a
			// descriptive message to the caller.
			m.close(fmt.Errorf("ws read: %w", err))
			return
		}
		var frame wsFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			continue // skip malformed single frame
		}
		switch frame.Type {
		case "connection_ack":
			markReady()
		case "ka", "pong", "ping":
			// keepalive; ignore.
		case "next":
			if sub := m.lookup(frame.ID); sub != nil {
				// Non-blocking send so one slow consumer can't stall
				// the whole receiver. If the channel is full we drop
				// the frame — the next one will arrive shortly.
				select {
				case sub.framesCh <- frame.Payload:
				default:
				}
			}
		case "error":
			if sub := m.lookup(frame.ID); sub != nil {
				m.deliverSubErr(sub, fmt.Errorf("ws server error: %s", string(frame.Payload)))
			}
		case "complete":
			if sub := m.lookup(frame.ID); sub != nil {
				m.deliverSubErr(sub, errors.New("ws subscription completed by server"))
			}
		case "connection_error":
			var termErr error
			if strings.Contains(string(frame.Payload), "Unauthenticated") {
				termErr = errWSUnauthenticated
			} else {
				termErr = fmt.Errorf("ws connection_error: %s", string(frame.Payload))
			}
			m.close(termErr)
			return
		default:
			// Unknown frame type — skip.
		}
	}
}

// lookup returns the sub registered under id, or nil.
func (m *wsMux) lookup(id string) *wsSub {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subs[id]
}

// deliverSubErr sends a terminal error to sub.errCh once, then
// removes it from the dispatch table.
func (m *wsMux) deliverSubErr(sub *wsSub, err error) {
	m.mu.Lock()
	delete(m.subs, sub.id)
	m.mu.Unlock()
	select {
	case sub.errCh <- err:
	default:
	}
}

// close tears down the mux. Delivers err to every active sub, closes
// the conn, and marks the mux as shutdown so further subscribe()
// calls fail fast. Idempotent.
func (m *wsMux) close(err error) {
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return
	}
	m.shutdown = true
	if err != nil && m.err == nil {
		m.err = err
	}
	subs := make([]*wsSub, 0, len(m.subs))
	for _, s := range m.subs {
		subs = append(subs, s)
	}
	m.subs = map[string]*wsSub{}
	m.mu.Unlock()

	termErr := err
	if termErr == nil {
		termErr = errors.New("ws mux: closed")
	}
	for _, s := range subs {
		select {
		case s.errCh <- termErr:
		default:
		}
	}
	if m.conn != nil {
		_ = m.conn.Close(websocket.StatusNormalClosure, "bye")
	}
}
