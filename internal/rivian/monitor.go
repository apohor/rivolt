package rivian

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// StateMonitor maintains a websocket subscription per vehicle and
// caches the latest pushed State. Callers read via Latest(); the
// subscription keeps it fresh in the background. Use Start() once per
// process to kick off the monitoring goroutines.
//
// Intended usage: one StateMonitor wrapping the live Client. The HTTP
// handler calls monitor.Latest(vehicleID) instead of client.State(),
// getting a cache-hit response without upstream cost. Missing-entry
// reads trigger a REST fallback to prime the cache while the
// subscription catches up.
type StateMonitor struct {
	client *LiveClient
	logger *slog.Logger

	mu       sync.RWMutex
	cache    map[string]*State
	stamp    map[string]time.Time
	active   map[string]context.CancelFunc
	parent   context.Context //nolint:containedctx // outer ctx for spawned subscriptions
	stopOnce sync.Once
}

// NewStateMonitor wraps a live client. Pass a logger (usually from
// main.go's structured logger). nil is allowed; events will be
// discarded.
func NewStateMonitor(client *LiveClient, logger *slog.Logger) *StateMonitor {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return &StateMonitor{
		client: client,
		logger: logger,
		cache:  make(map[string]*State),
		stamp:  make(map[string]time.Time),
		active: make(map[string]context.CancelFunc),
	}
}

// Start binds the monitor to a parent context. All subscriptions
// started via EnsureSubscribed use a child context derived from this
// parent; cancelling parent tears them all down.
func (m *StateMonitor) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parent = ctx
}

// EnsureSubscribed guarantees a background subscription exists for
// the given vehicle. Safe to call concurrently; the first caller
// wins, subsequent callers are no-ops. If the subscription dies
// (e.g. ctx cancelled after an unauthenticated error) it stays
// removed so a future call can retry.
func (m *StateMonitor) EnsureSubscribed(vehicleID string) {
	m.mu.Lock()
	if _, exists := m.active[vehicleID]; exists {
		m.mu.Unlock()
		return
	}
	if m.parent == nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.active[vehicleID] = cancel
	m.mu.Unlock()

	go m.run(ctx, vehicleID)
}

// run is the per-vehicle subscription goroutine. It blocks inside
// SubscribeVehicleState, which internally reconnects with backoff on
// transient errors, and only returns when ctx is cancelled or the
// server rejects the session token.
func (m *StateMonitor) run(ctx context.Context, vehicleID string) {
	m.logger.Info("rivian ws subscribe", "vehicle", vehicleID)
	err := m.client.SubscribeVehicleState(ctx, vehicleID, func(st *State) {
		m.mu.Lock()
		m.cache[vehicleID] = st
		m.stamp[vehicleID] = time.Now()
		m.mu.Unlock()
	})
	m.mu.Lock()
	delete(m.active, vehicleID)
	m.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		m.logger.Warn("rivian ws subscribe ended", "vehicle", vehicleID, "err", err.Error())
	}
}

// Latest returns the most recently pushed state for a vehicle, along
// with the wall-clock time it was received, or (nil, zero) if nothing
// has arrived yet.
func (m *StateMonitor) Latest(vehicleID string) (*State, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cache[vehicleID], m.stamp[vehicleID]
}

// Prime stores a state from an out-of-band source (typically a REST
// fallback on first request) so subsequent Latest() calls return it
// immediately while the subscription is still spinning up.
func (m *StateMonitor) Prime(vehicleID string, st *State) {
	if st == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[vehicleID] = st
	m.stamp[vehicleID] = time.Now()
}

// discardWriter is an io.Writer that eats everything, used as a
// default slog sink when no logger is supplied.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
