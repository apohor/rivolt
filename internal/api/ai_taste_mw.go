package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/apohor/rivolt/internal/ai"
	"github.com/apohor/rivolt/internal/aitaste"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
)

// requireAITasteMW marks the request with the metering the operator key applies (ai.WithTaste). A Pro account carries ai.NoTaste: it is
// bounded by the daily budget alone. A free account carries a gate that takes one of its monthly answers, plus a hold on the install-wide
// daily ceiling, at the moment the operator key is about to be called: so a cached answer, a page load, a refused request (consent comes
// first) and a provider failure (the unit is refunded) spend nothing. A user the middleware cannot place, a plan it cannot read and a
// store that is missing carry no mark at all, and the operator key then refuses the call. It must sit after the AI consent and budget gates.
func requireAITasteMW(ent *entitlements.Service, tastes *aitaste.Store, spend *aitaste.Spend) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := auth.UserFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			st, err := ent.State(r.Context(), uid)
			if err != nil {
				slog.WarnContext(r.Context(), "ai taste: plan unreadable; operator key stays closed", "err", err.Error())
				next.ServeHTTP(w, r)
				return
			}
			if st.Pro {
				next.ServeHTTP(w, r.WithContext(ai.WithTaste(r.Context(), ai.NoTaste)))
				return
			}
			if tastes == nil || spend == nil {
				next.ServeHTTP(w, r)
				return
			}
			g := &aitaste.Gate{Store: tastes, Spend: spend, User: uid, Quota: aitaste.MonthlyQuota}
			next.ServeHTTP(w, r.WithContext(ai.WithTaste(r.Context(), g)))
		})
	}
}

// writeAITasteError answers a call the taste refused: 402 ai_taste_used with when the month renews, or 503 with when the daily ceiling does.
// It reports whether err was one of those.
func writeAITasteError(w http.ResponseWriter, err error) bool {
	now := time.Now()
	switch {
	case errors.Is(err, ai.ErrTasteUsed):
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error":    "ai_taste_used",
			"reset_at": aitaste.ResetAt(now).Format(time.RFC3339),
		})
		return true
	case errors.Is(err, ai.ErrTasteCeiling):
		next := aitaste.NextDay(now)
		w.Header().Set("Retry-After", strconv.Itoa(int(next.Sub(now).Seconds())+1))
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":    "ai_taste_unavailable",
			"reset_at": next.Format(time.RFC3339),
		})
		return true
	}
	return false
}
