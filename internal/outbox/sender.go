package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// leaseMargin is how much earlier than the lease a handler's context ends, so MarkSent lands inside the claim.
const leaseMargin = 15 * time.Second

// ErrMoot is a Handler's answer when the intent no longer applies (the session is gone, the effect already happened): the row is set aside as
// failed without another try.
var ErrMoot = errors.New("outbox: intent is moot")

// Handler performs one intent's side effect. It must be safe to run twice for one row (delivery is at-least-once): the effect keeps its own claim.
// nil marks the row sent, ErrMoot sets it aside, any other error releases it for a later try.
type Handler func(ctx context.Context, r Row) error

// Sender claims due rows of the kinds it has a handler for and runs the handlers. With no handler registered it claims nothing, so shadow rows
// written before any consumer exists stay untouched until Purge expires them.
type Sender struct {
	store *Store
	owner string

	// Interval is the wait between ticks, Batch the rows per tick, Lease how long a claim holds (it must outlast a handler).
	Interval time.Duration
	Batch    int
	Lease    time.Duration

	mu       sync.Mutex
	handlers map[string]Handler

	processed *prometheus.CounterVec
}

// NewSender returns a sender on store. reg may be nil (tests).
func NewSender(store *Store, reg prometheus.Registerer) *Sender {
	host, _ := os.Hostname()
	s := &Sender{
		store: store, owner: fmt.Sprintf("%s/%d", host, os.Getpid()),
		Interval: 5 * time.Second, Batch: 20, Lease: 2 * time.Minute,
		handlers: map[string]Handler{},
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rivolt_outbox_processed_total",
			Help: "Outbox rows the sender handled, by kind and result (sent, retry, dropped)."}, []string{"kind", "result"}),
	}
	for k := range kinds {
		for _, r := range []string{"sent", "retry", "dropped"} {
			s.processed.WithLabelValues(k, r).Add(0)
		}
	}
	if reg != nil {
		reg.MustRegister(s.processed)
	}
	return s
}

// Register sets the handler for kind. Register before Run.
func (s *Sender) Register(kind string, h Handler) {
	if _, ok := kinds[kind]; !ok {
		panic("outbox: unknown kind " + kind)
	}
	s.mu.Lock()
	s.handlers[kind] = h
	s.mu.Unlock()
}

func (s *Sender) registered() ([]string, map[string]Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ks := make([]string, 0, len(s.handlers))
	hs := make(map[string]Handler, len(s.handlers))
	for k, h := range s.handlers {
		ks = append(ks, k)
		hs[k] = h
	}
	sort.Strings(ks)
	return ks, hs
}

// Once runs one tick and returns the rows it handled. Safe on any number of pods: a claim is SKIP LOCKED and leased.
func (s *Sender) Once(ctx context.Context) (int, error) {
	ks, hs := s.registered()
	rows, err := s.store.ClaimKinds(ctx, s.owner, ks, s.Batch, s.Lease)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		s.handle(ctx, r, hs[r.Kind])
	}
	return len(rows), nil
}

func (s *Sender) handle(ctx context.Context, r Row, h Handler) {
	// A stale intent is dropped, not sent late (the kind's MaxAge).
	if age, ok := MaxAge[r.Kind]; ok && time.Since(r.CreatedAt) > age {
		s.finish(ctx, r, "dropped", s.store.Drop(ctx, r.ID, s.owner))
		return
	}
	// The handler must finish before its lease can expire, or a second sender reclaims the row and sends it again.
	hctx, cancel := context.WithTimeout(ctx, s.Lease-leaseMargin)
	err := h(hctx, r)
	cancel()
	switch {
	case err == nil:
		s.finish(ctx, r, "sent", s.store.MarkSent(ctx, r.ID, s.owner))
	case errors.Is(err, ErrMoot):
		s.finish(ctx, r, "dropped", s.store.Drop(ctx, r.ID, s.owner))
	default:
		// A provider error can echo an address or token: log the row, not the error text.
		slog.WarnContext(ctx, "outbox send failed; will retry", "id", r.ID, "kind", r.Kind, "attempts", r.Attempts)
		s.finish(ctx, r, "retry", s.store.Release(ctx, r.ID, s.owner))
	}
}

func (s *Sender) finish(ctx context.Context, r Row, result string, err error) {
	if err != nil {
		// ErrNotClaimed: the lease expired and another sender has the row; its claim decides.
		slog.WarnContext(ctx, "outbox row not recorded", "id", r.ID, "kind", r.Kind, "result", result, "err_not_claimed", errors.Is(err, ErrNotClaimed))
		return
	}
	s.processed.WithLabelValues(r.Kind, result).Inc()
}

// Run ticks until ctx ends.
func (s *Sender) Run(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.Once(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "outbox sender tick failed", "err", err)
			}
		}
	}
}
