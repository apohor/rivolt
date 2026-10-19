package rivian

import (
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"
)

// WSFrameDropObserver, when set (once, at startup, before serving), is told
// each time a received "next" frame is dropped because its subscription's
// channel was full; main counts them. Never given an id.
var WSFrameDropObserver func()

// wsDropLogEvery bounds the Warn line: a stalled consumer drops every frame.
const wsDropLogEvery = time.Minute

var wsDropLastLog atomic.Int64 // unix nanos of the last Warn

// deliverFrame hands one payload to a subscription without blocking the single
// reader goroutine. A full channel drops the frame (the next one follows
// shortly) but the drop is counted and logged at Warn, once a minute: a slow
// database makes the consumer hold the recorder lock, and the frames dropped
// meanwhile can carry a gear transition (review §4.2).
func (m *wsMux) deliverFrame(sub *wsSub, payload json.RawMessage) bool {
	select {
	case sub.framesCh <- payload:
		return true
	default:
	}
	if f := WSFrameDropObserver; f != nil {
		f()
	}
	now := time.Now().UnixNano()
	if last := wsDropLastLog.Load(); now-last >= int64(wsDropLogEvery) && wsDropLastLog.CompareAndSwap(last, now) {
		slog.Warn("ws frame dropped: subscription consumer is behind", "sub_buffer", cap(sub.framesCh))
	}
	return false
}
