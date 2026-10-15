package push

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Network-confirm pushes: after a DC fast charge is auto-priced, ask
// the user to confirm or change the detected network. One push per
// charge; at most confirmPerWindow per user per confirmWindow, after
// which a single "N charges to confirm" push stands in for the rest.
const (
	confirmPerWindow = 3
	confirmWindow    = time.Hour
	// confirmURLFmt is where the push opens: the charge's pricing card.
	confirmURLFmt = "/charges/%s#pricing"
)

// NetworkConfirm is what the close hook knows about an auto-priced
// charge. Cost/Rate are zero when nothing matched.
type NetworkConfirm struct {
	ChargeID string
	// Network is the detected network's display name ("" if unmatched).
	Network string
	// Other is a second, co-located network, if the evidence has one.
	Other string
	Cost  float64
	Rate  float64
	KWh   float64
	// Matched is false when nothing was within range (no price).
	Matched bool
	// State is "estimated" or "needs_confirmation".
	State string
}

// NetworkConfirmPayload builds the notification for e, using the
// contract's three wordings.
func NetworkConfirmPayload(e NetworkConfirm) Payload {
	var body string
	switch {
	case !e.Matched:
		body = fmt.Sprintf("Fast charge (%.0f kWh) couldn't be matched to a network. Pick one", e.KWh)
	case e.State == "needs_confirmation" && e.Other != "":
		body = fmt.Sprintf("Fast charge priced $%.2f as %s; %s is here too. Confirm or change", e.Cost, e.Network, e.Other)
	default:
		body = fmt.Sprintf("Fast charge at %s priced $%.2f ($%.2f/kWh). Confirm or change", e.Network, e.Cost, e.Rate)
	}
	return Payload{
		Title: "Confirm charging network",
		Body:  body,
		Tag:   "rivolt-network-confirm-" + e.ChargeID,
		URL:   fmt.Sprintf(confirmURLFmt, e.ChargeID),
		Kind:  "network_confirm",
	}
}

// confirmLimiter allows confirmPerWindow sends per key per window and
// reports the first denial of each window so the caller can send the
// collapsed summary once.
type confirmLimiter struct {
	mu   sync.Mutex
	now  func() time.Time
	sent map[string][]time.Time
	over map[string]time.Time // window start of the last summary
}

func newConfirmLimiter() *confirmLimiter {
	return &confirmLimiter{now: time.Now, sent: map[string][]time.Time{}, over: map[string]time.Time{}}
}

// take returns (send, summary): send means deliver the individual push;
// summary means this is the first overflow in the window.
func (l *confirmLimiter) take(key string) (send, summary bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cut := now.Add(-confirmWindow)
	kept := l.sent[key][:0]
	for _, t := range l.sent[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) < confirmPerWindow {
		l.sent[key] = append(kept, now)
		return true, false
	}
	l.sent[key] = kept
	if last, ok := l.over[key]; ok && last.After(cut) {
		return false, false
	}
	l.over[key] = now
	return false, true
}

var networkConfirmLimiter = newConfirmLimiter()

// selectNetworkConfirm reuses the charging-done preference: someone who
// opted out of charge notifications gets no confirm prompts either.
func selectNetworkConfirm(s Subscription) bool { return s.OnChargingDone }

// NotifyNetworkConfirm sends the confirm-or-change push for an
// auto-priced DC fast charge, rate limited per user. userKey scopes the
// limiter (the user id); pending is how many charges the user has open
// for confirmation, used in the collapsed summary. The limiter is
// per-process: with several pods a user can see up to confirmPerWindow
// per pod.
func (s *Service) NotifyNetworkConfirm(store *Store, userKey string, e NetworkConfirm, pending int) {
	if s == nil || store == nil {
		return
	}
	send, summary := networkConfirmLimiter.take(userKey)
	switch {
	case send:
		go s.broadcast(store, NetworkConfirmPayload(e), selectNetworkConfirm)
	case summary:
		if pending < 1 {
			pending = 1
		}
		noun := "charges"
		if pending == 1 {
			noun = "charge"
		}
		go s.broadcast(store, Payload{
			Title: "Confirm charging network",
			Body:  fmt.Sprintf("%d %s to confirm", pending, noun),
			Tag:   "rivolt-network-confirm-summary",
			URL:   "/money",
			Kind:  "network_confirm",
		}, selectNetworkConfirm)
	}
}

// CandidateOther picks the second distinct network from detection
// evidence candidates (names in nearest-first order); "" when none.
func CandidateOther(primary string, names []string) string {
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" && !strings.EqualFold(n, primary) {
			return n
		}
	}
	return ""
}
