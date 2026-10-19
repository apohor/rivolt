package rivian

import (
	"context"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/samples"
)

// Catch-up write batching for source-fed (replay) monitors.
//
// While a FrameSource replays already-elapsed frames (catchingUp, the
// same signal that silences push notifications), the recorder would
// otherwise pay several serial round trips per frame: a single-row
// vehicle_state insert (partition check + tx + prepare + exec +
// commit), a Redis live-state snapshot, and a drive/charge upsert on
// every in-session frame. A 30-day backfill is ~370k frames, so that
// latency, not CPU, bounded catch-up at ~43 frames/s (about 2 h).
//
// During catch-up those three writes are buffered per vehicle and
// flushed together: samples as multi-row inserts, only the latest
// in-session row per drive/charge id, and only the latest snapshot.
// Session opens and closes still write through immediately, so the
// database always holds every closed session's final state (and
// resumeOpenCharge never reattaches a session that already closed).
//
// The buffer is flushed when it reaches catchupFlushSamples, before the
// first live frame, and when the stream ends (lease loss, shutdown).
// A crash before a flush loses at most one buffer: the replay resumes
// from the last persisted sample and the Redis snapshot written at the
// same flush, so the lost frames are replayed, not skipped.
//
// Live monitors have no catchupWriter, so nothing here touches the
// live WebSocket path.

// catchupFlushSamples is how many buffered samples trigger a flush.
const catchupFlushSamples = 500

// catchupFlushTimeout bounds one flush; it runs on a detached context
// so a cancelled stream still persists what it buffered.
const catchupFlushTimeout = 30 * time.Second

type catchupBuffer struct {
	samples []samples.Sample
	drives  map[string]*liveDrive
	charges map[string]*liveCharge
	snap    *LiveStateSnapshot
}

func (b *catchupBuffer) empty() bool {
	return b == nil || (len(b.samples) == 0 && len(b.drives) == 0 && len(b.charges) == 0 && b.snap == nil)
}

// catchupSinks are the writes a flush performs. Defaults call the
// monitor's stores; tests substitute recorders.
type catchupSinks struct {
	insertSamples func(ctx context.Context, batch []samples.Sample) error
	upsertDrive   func(ctx context.Context, vehicleID string, d *liveDrive)
	upsertCharge  func(ctx context.Context, vehicleID string, c *liveCharge)
	saveSnapshot  func(vehicleID string, snap LiveStateSnapshot)
}

type catchupWriter struct {
	mu    sync.Mutex
	bufs  map[string]*catchupBuffer
	sinks catchupSinks
	// retryAt holds back size-triggered flushes for a vehicle after a
	// failed sample insert, so a database outage isn't hit on every
	// frame. Transition and stream-end flushes always try.
	retryAt map[string]time.Time
}

// catchupRetryBackoff is how long size-triggered flushes wait after a
// failed one.
const catchupRetryBackoff = 10 * time.Second

func newCatchupWriter(m *StateMonitor) *catchupWriter {
	return &catchupWriter{
		bufs:    map[string]*catchupBuffer{},
		retryAt: map[string]time.Time{},
		sinks: catchupSinks{
			insertSamples: func(ctx context.Context, batch []samples.Sample) error {
				if m.samplesStore == nil {
					return nil
				}
				return m.samplesStore.InsertBulk(ctx, batch)
			},
			upsertDrive:  m.upsertLiveDrive,
			upsertCharge: m.upsertLiveCharge,
			saveSnapshot: m.persistLiveState,
		},
	}
}

func (w *catchupWriter) buf(vehicleID string) *catchupBuffer {
	b := w.bufs[vehicleID]
	if b == nil {
		b = &catchupBuffer{drives: map[string]*liveDrive{}, charges: map[string]*liveCharge{}}
		w.bufs[vehicleID] = b
	}
	return b
}

func (w *catchupWriter) addSample(vehicleID string, s samples.Sample) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buf(vehicleID)
	b.samples = append(b.samples, s)
	return len(b.samples)
}

func (w *catchupWriter) markDrive(vehicleID string, d *liveDrive) {
	w.mu.Lock()
	w.buf(vehicleID).drives[d.id] = d
	w.mu.Unlock()
}

func (w *catchupWriter) markCharge(vehicleID string, c *liveCharge) {
	w.mu.Lock()
	w.buf(vehicleID).charges[c.id] = c
	w.mu.Unlock()
}

func (w *catchupWriter) setSnapshot(vehicleID string, snap LiveStateSnapshot) {
	w.mu.Lock()
	w.buf(vehicleID).snap = &snap
	w.mu.Unlock()
}

// forgetDrive / forgetCharge drop a pending in-session row because a
// write-through (open, close, stale close) just persisted a newer one.
func (w *catchupWriter) forgetDrive(vehicleID, id string) {
	w.mu.Lock()
	if b := w.bufs[vehicleID]; b != nil {
		delete(b.drives, id)
	}
	w.mu.Unlock()
}

func (w *catchupWriter) forgetCharge(vehicleID, id string) {
	w.mu.Lock()
	if b := w.bufs[vehicleID]; b != nil {
		delete(b.charges, id)
	}
	w.mu.Unlock()
}

func (w *catchupWriter) pendingSamples(vehicleID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if b := w.bufs[vehicleID]; b != nil {
		return len(b.samples)
	}
	return 0
}

// requeue puts samples (and the snapshot taken at the same point) back
// at the front of vehicleID's buffer after a failed flush, ahead of
// anything buffered since, so the next flush writes them in order.
func (w *catchupWriter) requeue(vehicleID string, batch []samples.Sample, snap *LiveStateSnapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buf(vehicleID)
	b.samples = append(batch, b.samples...)
	if b.snap == nil {
		b.snap = snap
	}
	w.retryAt[vehicleID] = time.Now().Add(catchupRetryBackoff)
}

// flushDue reports whether a size-triggered flush may run now.
func (w *catchupWriter) flushDue(vehicleID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !time.Now().Before(w.retryAt[vehicleID])
}

func (w *catchupWriter) take(vehicleID string) *catchupBuffer {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.bufs[vehicleID]
	delete(w.bufs, vehicleID)
	return b
}

// deferringWrites reports whether recorder writes are being buffered:
// a source monitor replaying already-elapsed frames.
func (m *StateMonitor) deferringWrites() bool {
	return m.catchup != nil && m.quietHooks.Load()
}

// touchLiveDrive is the in-session ("periodic re-upsert") write for a
// drive. Buffered during catch-up, immediate otherwise.
func (m *StateMonitor) touchLiveDrive(ctx context.Context, vehicleID string, d *liveDrive) {
	if d != nil && m.deferringWrites() {
		m.catchup.markDrive(vehicleID, d)
		return
	}
	if d != nil && !d.touch.due(liveTouchNow()) {
		return
	}
	m.upsertLiveDrive(ctx, vehicleID, d)
}

// touchLiveCharge is touchLiveDrive for charges.
func (m *StateMonitor) touchLiveCharge(ctx context.Context, vehicleID string, c *liveCharge) {
	if c != nil && m.deferringWrites() {
		m.catchup.markCharge(vehicleID, c)
		return
	}
	if c != nil && !c.touch.due(liveTouchNow()) {
		return
	}
	m.upsertLiveCharge(ctx, vehicleID, c)
}

// flushCatchup writes everything buffered for vehicleID: in-session
// drive/charge rows first, then samples, then the live-state snapshot,
// so the snapshot never runs ahead of the persisted samples the replay
// resumes from. Must be called from the vehicle's stream goroutine
// (the only writer of its accumulators).
func (m *StateMonitor) flushCatchup(vehicleID string) {
	if m.catchup == nil {
		return
	}
	b := m.catchup.take(vehicleID)
	if b.empty() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), catchupFlushTimeout)
	defer cancel()
	sinks := m.catchup.sinks
	for _, d := range b.drives {
		sinks.upsertDrive(ctx, vehicleID, d)
	}
	for _, c := range b.charges {
		sinks.upsertCharge(ctx, vehicleID, c)
	}
	if len(b.samples) > 0 {
		if err := sinks.insertSamples(ctx, b.samples); err != nil {
			// Keep the batch (and the snapshot taken with it) for the
			// next flush rather than dropping it: later flushes would
			// otherwise move the persisted max(at) past these frames
			// and the replay would never resume over the hole.
			m.logger.Error("replay catch-up sample flush failed; keeping the batch for the next flush",
				"vehicle", vehicleID, "samples", len(b.samples), "err", err.Error())
			m.catchup.requeue(vehicleID, b.samples, b.snap)
			return
		}
	}
	if b.snap != nil {
		sinks.saveSnapshot(vehicleID, *b.snap)
	}
}
