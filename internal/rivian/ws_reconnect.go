package rivian

import (
	"sync/atomic"
	"time"
)

// The reasons a vehicleState subscription is opened again, as rivolt_rivian_ws_reconnects_total{reason} counts them (a small fixed set):
const (
	WSReconnectNudge    = "nudge"     // a periodic refresh asked for a fresh socket (a freshly woken car)
	WSReconnectWatchdog = "watchdog"  // no push for the stale threshold: the watchdog killed the socket
	WSReconnectNoFrames = "no_frames" // the socket ran long enough and never delivered a frame
	WSReconnectError    = "error"     // the subscription ended with an error
	WSReconnectClean    = "clean"     // the subscription returned without one
)

// WSReconnectReasons is every reason, for the counter's zero-fill.
var WSReconnectReasons = []string{WSReconnectNudge, WSReconnectWatchdog, WSReconnectNoFrames, WSReconnectError, WSReconnectClean}

var wsReconnectObserver atomic.Pointer[func(reason string)]

// SetWSReconnectObserver installs the counter hook (main, once, at boot; nil removes it).
func SetWSReconnectObserver(fn func(reason string)) {
	if fn == nil {
		wsReconnectObserver.Store(nil)
		return
	}
	wsReconnectObserver.Store(&fn)
}

func noteWSReconnect(reason string) {
	if fn := wsReconnectObserver.Load(); fn != nil {
		(*fn)(reason)
	}
}

// wsReconnectReason classifies why a subscription ended and will be opened again, in the same order as the loop's log branches: a nudge first, then
// a zombie (the watchdog's kill, or a socket that never delivered a frame for half the stale threshold), then an error, else a clean return.
func wsReconnectReason(nudged, watchdogKilled bool, frames int64, sessionDur time.Duration, err error) string {
	switch {
	case nudged:
		return WSReconnectNudge
	case watchdogKilled:
		return WSReconnectWatchdog
	case frames == 0 && sessionDur > wsStaleThreshold/2:
		return WSReconnectNoFrames
	case err != nil:
		return WSReconnectError
	}
	return WSReconnectClean
}
