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

func TestWSCloseClass(t *testing.T) {
	for code, want := range map[websocket.StatusCode]string{
		4420: "success", websocket.StatusNormalClosure: "success",
		websocket.StatusTryAgainLater: "rate_limited", 4401: "user_action", 4410: "unknown",
		-1: "transient", websocket.StatusGoingAway: "transient", 4999: "unknown",
	} {
		if got := wsCloseClass(code); got != want {
			t.Errorf("close %d: class %q, want %q", code, got, want)
		}
	}
}

// RATE_LIMIT is the code the Rivian app knows; it is a rate limit, not an unknown error.
func TestGraphQLRateLimitCode(t *testing.T) {
	for _, code := range []string{"RATE_LIMIT", "rate_limit", "RATE_LIMITED", "THROTTLED"} {
		if c, _ := ClassifyGraphQL(code, "anything"); c != ClassRateLimited {
			t.Errorf("%s: class %v, want rate limited", code, c)
		}
	}
}

// A connection the server closes with 1013 after the ack is counted as a rate limit under op "ws_close".
func TestMuxCountsTheCloseCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		ctx := r.Context()
		if _, _, err := c.Read(ctx); err != nil { // connection_init
			return
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"connection_ack"}`))
		_ = c.Close(websocket.StatusTryAgainLater, "slow down")
	}))
	defer srv.Close()
	rec := &durationRecorder{}
	c := NewLive().WithResultObserver(rec)
	c.userSessionToken = "u"
	c.wsEndpointOverride = "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.acquireMux(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		n := len(rec.results)
		got := append([]string(nil), rec.results...)
		rec.mu.Unlock()
		if n > 0 {
			for _, r := range got {
				if r == "ws_close/rate_limited" {
					return
				}
			}
			t.Fatalf("results %v, want ws_close/rate_limited", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no close was counted")
}
