package api

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/billingmail"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/launchnotice"
)

// startDatedNoticeJob runs the dated Pro notices hourly for the life of the process: the founding-year-end notice (at least 30 days before
// each founding year ends; a year found under 30 days from its end is extended to 30 days from now, counted as late) and, once a source
// is wired by the billing item, the yearly-renewal reminder. Safe on every replica: the claim is a database row, and a year is extended
// in the same transaction that claims its email. Service notices: they ignore engagement settings. Nothing happens until an account has a
// founding row, so a self-hosted install (no billing, no founding grants) never sends anything.
func startDatedNoticeJob(d Deps) {
	skip := map[uuid.UUID]bool{demo.UserID(): true}
	if bu := strings.TrimSpace(os.Getenv("RIVOLT_AUTH_BYPASS_USER")); bu != "" {
		skip[db.UserIDFor(bu)] = true
	}
	observe := func(kind, outcome string) {
		if d.Metrics != nil {
			d.Metrics.DatedNoticesTotal.WithLabelValues(kind, outcome).Inc()
		}
	}
	founding := &launchnotice.DatedJob{
		Store: launchnotice.PGDated{DB: d.SystemDB}, Mailer: d.Email, Logger: d.Logger, Skip: skip, Observe: observe,
		OnExtend: func(uid uuid.UUID) {
			if d.Entitlements != nil {
				d.Entitlements.Invalidate(uid)
			}
		},
	}
	// The renewal reminder reads the annual Stripe subscriptions (billingmail.RenewalSource); with none, nothing.
	renewals := &launchnotice.RenewalJob{Source: billingmail.RenewalSource{DB: d.SystemDB}, Store: launchnotice.Store{DB: d.SystemDB}, Mailer: d.Email, Logger: d.Logger, Skip: skip, Observe: observe}
	// The transactional billing emails (purchase confirmation, payment failed), caught up every few minutes from the state the Stripe webhook
	// wrote: a nil mailer sends nothing, and an install with no Stripe subscriptions has nothing to send.
	billing := &billingmail.Job{DB: d.SystemDB, Mailer: d.Email, Logger: d.Logger, Skip: skip, Observe: observe}
	go func() {
		t := time.NewTimer(3 * time.Minute)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if st, err := billing.RunOnce(ctx); err != nil {
				slog.Warn("billing email run failed", "err", err.Error())
			} else if st.Considered > 0 {
				slog.Info("billing email run", "considered", st.Considered, "sent", st.Sent, "failed", st.Failed, "skipped", st.Skipped)
			}
			cancel()
			t.Reset(5 * time.Minute)
		}
	}()
	go func() {
		t := time.NewTimer(2 * time.Minute) // let the pod settle first
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			if st, err := founding.RunOnce(ctx); err != nil {
				slog.Warn("founding notice run failed", "err", err.Error())
			} else if st.Considered > 0 {
				slog.Info("founding notice run", "considered", st.Considered, "sent", st.Sent, "in_app", st.InApp, "failed", st.Failed,
					"skipped", st.Skipped, "late", st.Late, "worst_shortfall_days", int(st.WorstShortfall.Hours()/24))
			}
			if st, err := renewals.RunOnce(ctx); err != nil {
				slog.Warn("renewal reminder run failed", "err", err.Error())
			} else if st.Considered > 0 {
				slog.Info("renewal reminder run", "considered", st.Considered, "sent", st.Sent, "failed", st.Failed, "late", st.Late)
			}
			cancel()
			t.Reset(time.Hour)
		}
	}()
}
