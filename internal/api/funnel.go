package api

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/metrics"
)

// Signup-to-first-vehicle funnel events. Each one marks a step a user
// reached on the way from "account created" to "Rivolt is recording
// their truck". Most signups that never add a vehicle stall somewhere
// in here, and without these we could only guess where: the per-user
// trail is otherwise reconstructable from request logs alone, which
// don't outlive Loki retention.
//
// Keep this list the whole vocabulary - it becomes a Prometheus
// label, so it must stay small and fixed.
const (
	funnelSignupCreated       = "signup_created"
	funnelOnboardingComplete  = "onboarding_completed"
	funnelOnboardingSkipped   = "onboarding_skipped_connect"
	funnelRivianLoginAttempt  = "rivian_login_attempt"
	funnelRivianLoginFailed   = "rivian_login_failed"
	funnelRivianMFARequired   = "rivian_mfa_required"
	funnelRivianMFAFailed     = "rivian_mfa_failed"
	funnelRivianConnected     = "rivian_connected"
	funnelRivianNoVehicles    = "rivian_no_vehicles"
	funnelRivianVehicleCheck  = "rivian_vehicle_recheck"
	funnelRivianVehiclesFound = "rivian_vehicles_found"
)

// recordFunnel bumps the funnel counter and writes one structured
// "funnel" log line carrying the user id, so a single LogQL query
// (`|= "\"msg\":\"funnel\""`) replays any user's path. Safe with a nil
// Metrics (tests, stub mode).
func recordFunnel(ctx context.Context, m *metrics.Metrics, uid uuid.UUID, event string, attrs ...any) {
	if m != nil {
		m.FunnelEventsTotal.WithLabelValues(event).Inc()
	}
	args := append([]any{"event", event, "user_id", uid.String()}, attrs...)
	slog.InfoContext(ctx, "funnel", args...)
}
