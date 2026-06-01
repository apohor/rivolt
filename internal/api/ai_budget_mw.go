package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/apohor/rivolt/internal/aibudget"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/flags"
)

// requireAIBudgetMW charges one unit of the per-user daily AI-call
// budget before the wrapped LLM endpoint runs, and 429s when the user
// is over the operator-set cap (flags.AICallCapName). It is a cost
// backstop, so it fails OPEN: a non-positive cap, a missing user on
// the context, or a counter-store error all let the request through —
// breaking the feature to protect the budget would be the wrong trade.
func requireAIBudgetMW(fl *flags.Store, budget *aibudget.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if fl == nil || budget == nil {
				next.ServeHTTP(w, r)
				return
			}
			limit := fl.AICallCap().DailyLimit
			if limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			uid, ok := auth.UserFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			allowed, used, err := budget.TryConsume(r.Context(), uid, limit)
			if err != nil {
				slog.WarnContext(r.Context(), "ai budget check failed; allowing", "err", err.Error())
				next.ServeHTTP(w, r)
				return
			}
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(secondsUntilUTCMidnight(time.Now())))
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error": "Daily AI request limit reached — try again tomorrow.",
					"limit": limit,
					"used":  used,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// secondsUntilUTCMidnight returns how many seconds remain until the
// counter resets (CURRENT_DATE rolls over at UTC midnight, matching
// the ai_call_usage.day column). Always at least 1.
func secondsUntilUTCMidnight(now time.Time) int {
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
	d := int(next.Sub(now).Seconds())
	if d < 1 {
		return 1
	}
	return d
}
