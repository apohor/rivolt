package rivian

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The subscription WebSocket handshake carries X-Rivolt-Version, the
// same value the REST calls send. Dials a local server with the real
// dial options and reads the header the server saw.
func TestWSDial_SendsRivoltVersion(t *testing.T) {
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	c := NewLive().WithRivoltVersion("v0.32.1+sha-abc1234")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), c.wsDialOptions())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	select {
	case h := <-got:
		if v := h.Get("X-Rivolt-Version"); v != "v0.32.1+sha-abc1234" {
			t.Fatalf("X-Rivolt-Version = %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never saw the handshake")
	}
}

// Same value as the REST path, including the "dev" default.
func TestWSDial_VersionMatchesREST(t *testing.T) {
	for _, v := range []string{"", "v1.2.3"} {
		c := NewLive().WithRivoltVersion(v)
		rest := http.Header{}
		c.applyBaseHeaders(rest)
		ws := c.wsDialOptions().HTTPHeader
		if rest.Get("X-Rivolt-Version") == "" || ws.Get("X-Rivolt-Version") != rest.Get("X-Rivolt-Version") {
			t.Errorf("version %q: ws %q vs rest %q", v, ws.Get("X-Rivolt-Version"), rest.Get("X-Rivolt-Version"))
		}
	}
	if o := NewLive().wsDialOptions(); len(o.Subprotocols) != 1 || o.Subprotocols[0] != "graphql-transport-ws" {
		t.Errorf("subprotocols = %v", o.Subprotocols)
	}
}
