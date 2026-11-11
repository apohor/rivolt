package rivian

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

// The call permit (single-owner M4-1b): the second line behind the monitor's owner guard. Every request the client sends to Rivian
// (a GraphQL call, the charging-endpoint probe, a WebSocket dial) must carry either the fence token of the group's owner or an explicit
// holder marker. A call with neither is one a non-owner should not be making: it is counted, and in enforce mode it is not sent.
//
// Modes (RIVOLT_RIVIAN_PERMIT): off (the default, nothing is checked), observe (count and log, send anyway) and enforce (refuse with
// ErrNoPermit). Observe goes first on preview and prod so a call site that was missed shows up as a counted finding instead of a broken
// recording; enforce comes after a clean week (DA, M4 design 1).

// ErrNoPermit means the request carried neither a fence token nor a holder marker, and the permit is enforced.
var ErrNoPermit = errors.New("rivian: no call permit (the pod does not hold the group)")

// PermitMode is how the client treats a request without a permit.
type PermitMode int32

const (
	PermitOff PermitMode = iota
	PermitObserve
	PermitEnforce
)

// ParsePermitMode reads RIVOLT_RIVIAN_PERMIT. Anything but "observe" or "enforce" is off.
func ParsePermitMode(s string) PermitMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "observe":
		return PermitObserve
	case "enforce":
		return PermitEnforce
	}
	return PermitOff
}

// Holders are the callers allowed to talk to Rivian without a group (DA, M4 design 1). The fence token is the permit of the group's owner;
// these are the ones with no group.
const (
	HolderLogin        = "login"         // the settings sign-in, first leg
	HolderMFA          = "mfa"           // the settings sign-in, second leg
	HolderLogout       = "logout"        // sign-out
	HolderDeleteRevoke = "delete_revoke" // the account-delete revoke
	HolderForwardRun   = "forward_run"   // the owner's forward runner, admitted on a held lease
	HolderMonitor      = "monitor"       // set per call, only by rivianCtx (and the subscription gate) when the owner guard allows that call; never run-wide
)

type permitKey struct{}

type permitGateKey struct{}

// WithCallPermitGate is WithCallPermit for a long-lived context (a subscription the client reconnects inside): allowed is asked at every
// request the context sends, so a pod that lost the group stops being permitted at the next reconnect instead of keeping the permit it
// was given at the start.
func WithCallPermitGate(ctx context.Context, holder string, allowed func() bool) context.Context {
	return context.WithValue(WithCallPermit(ctx, holder), permitGateKey{}, allowed)
}

// WithCallPermit marks ctx as belonging to one of the Holders.
func WithCallPermit(ctx context.Context, holder string) context.Context {
	return context.WithValue(ctx, permitKey{}, holder)
}

var (
	permitMode  atomic.Int32
	unpermitted atomic.Pointer[func(op string)]
	permitLogMu sync.Mutex
	permitLogAt = map[string]time.Time{}
)

// SetPermitMode sets the process-wide mode (main, once, at boot; tests).
func SetPermitMode(m PermitMode) { permitMode.Store(int32(m)) }

// PermitOps are the operation names a permit check can report: every GraphQL operation the client sends, the WebSocket dial and "other" for a
// name not in this list (so the counter's label set stays closed). TestPermitOpsCoverEveryOperation keeps it complete.
var PermitOps = []string{"__Introspect", "CreateCSRFToken", "GetChargingSchedule", "GetCompletedSessionSummaries", "GetDetailedSessionInfoByTxnId",
	"getLiveSessionHistory", "getOTAUpdateDetails", "getUserInfo", "getVehicleImages", "GetVehicleState", "IntrospectEnum", "IntrospectInput", "Login",
	"LoginWithOTP", "planTripWithMultiStop", "planTripWithMultiStopV2", "Probe", "SupportedFeatures", "ws_dial", "other"}

// PermitOp maps an operation name to its label value.
func PermitOp(op string) string {
	for _, k := range PermitOps {
		if k == op {
			return op
		}
	}
	return "other"
}

// SetUnpermittedObserver installs the counter hook; op is the GraphQL operation name or "ws_dial".
func SetUnpermittedObserver(fn func(op string)) {
	if fn == nil {
		unpermitted.Store(nil)
		return
	}
	unpermitted.Store(&fn)
}

// checkPermit is called at every point the client sends a request to Rivian.
func checkPermit(ctx context.Context, op string) error {
	mode := PermitMode(permitMode.Load())
	if mode == PermitOff {
		return nil
	}
	if _, ok := fence.From(ctx); ok {
		return nil
	}
	if _, ok := ctx.Value(permitKey{}).(string); ok {
		gate, _ := ctx.Value(permitGateKey{}).(func() bool)
		if gate == nil || gate() {
			return nil
		}
	}
	if fn := unpermitted.Load(); fn != nil {
		(*fn)(op)
	}
	// One line per operation a minute: enough to find the caller, not a flood.
	permitLogMu.Lock()
	last, seen := permitLogAt[op]
	now := time.Now()
	log := !seen || now.Sub(last) >= time.Minute
	if log {
		permitLogAt[op] = now
	}
	permitLogMu.Unlock()
	if log {
		slog.WarnContext(ctx, "rivian call without a permit", "op", op, "mode", mode.String())
	}
	if mode == PermitEnforce {
		return ErrNoPermit
	}
	return nil
}

func (m PermitMode) String() string {
	switch m {
	case PermitObserve:
		return "observe"
	case PermitEnforce:
		return "enforce"
	}
	return "off"
}
