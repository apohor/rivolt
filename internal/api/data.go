package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/electrafi"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/vehiclelease"
)

// handleImportElectrafi accepts one or more CSV files in a multipart
// upload under the field name "file" and streams each through the
// ElectraFi importer. Returns per-file results as JSON. The upload is
// rejected if any required store is unavailable; we don't want partial
// imports that silently drop samples or charge sessions.
//
// A 1 GiB cap guards against accidental large uploads; ElectraFi
// exports for a single month are typically 30-50 MiB.
func handleImportElectrafi(d Deps) http.HandlerFunc {
	const maxUpload = 1 << 30 // 1 GiB
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ds := d.Drives.For(uid)
		cs := d.Charges.For(uid)
		ss := d.Samples.For(uid)
		if ds == nil || cs == nil || ss == nil {
			http.Error(w, "import unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "parse upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		// vehicle_id is the rivian-gateway id the user picked in the
		// SPA. We validate ownership server-side so a crafted upload
		// can't land samples under another user's vehicle.
		rivianVehicleID := strings.TrimSpace(r.FormValue("vehicle_id"))
		if rivianVehicleID == "" {
			http.Error(w, "vehicle_id form field is required", http.StatusBadRequest)
			return
		}
		owns, oerr := db.OwnsRivianID(r.Context(), d.DB, uid, rivianVehicleID)
		if oerr != nil {
			http.Error(w, "vehicle ownership check: "+oerr.Error(), http.StatusInternalServerError)
			return
		}
		if !owns {
			// 404 (not 403) so an attacker probing vehicle ids can't
			// confirm whether a given id belongs to someone else.
			http.Error(w, "vehicle not found", http.StatusNotFound)
			return
		}
		files := r.MultipartForm.File["file"]
		if len(files) == 0 {
			http.Error(w, "no files uploaded under field 'file'", http.StatusBadRequest)
			return
		}
		imp := &electrafi.Importer{
			Drives:    ds,
			Charges:   cs,
			Samples:   ss,
			VehicleID: rivianVehicleID,
		}
		// Samples older than the install's retention would recreate a dropped
		// partition for the next sweep to delete; drives and charges still import.
		if d.SampleRetentionMonths > 0 {
			imp.SampleFrom = samples.RetentionCutoff(time.Now(), d.SampleRetentionMonths)
		}
		if v := r.FormValue("pack_kwh"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				imp.PackKWh = f
			}
		}
		// tz picks the timezone the CSV timestamps were recorded in;
		// ElectraFi exports are local-without-zone so parsing as UTC
		// (the pre-v0.4.2 default) shifts every row. Default to the
		// server's local zone, which matches the typical self-hosted
		// setup.
		tz := strings.TrimSpace(r.FormValue("tz"))
		if tz == "" {
			tz = "Local"
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			http.Error(w, "invalid tz "+strconv.Quote(tz)+": "+err.Error(), http.StatusBadRequest)
			return
		}
		imp.Location = loc

		// Stream results as NDJSON. Most default nginx setups
		// close idle upstream connections after ~60s, producing a 504
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no") // nginx hint
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		enc := json.NewEncoder(w)
		emit := func(v any) {
			_ = enc.Encode(v)
			if flusher != nil {
				flusher.Flush()
			}
		}
		emit(map[string]any{"event": "start", "files": len(files)})

		results := make([]electrafi.Result, 0, len(files))
		var firstErr string
		for i, fh := range files {
			f, err := fh.Open()
			if err != nil {
				if firstErr == "" {
					firstErr = fh.Filename + ": open: " + err.Error()
				}
				emit(map[string]any{"event": "error", "file": fh.Filename, "error": "open: " + err.Error()})
				continue
			}
			emit(map[string]any{"event": "file_start", "index": i, "file": fh.Filename})
			// Heartbeat inside a single file. Large CSVs have 20k+
			// rows and can easily spend >60s parsing + inserting;
			// without an in-flight progress line the proxy idles out.
			idx := i
			name := fh.Filename
			imp.OnProgress = func(phase string, n int) {
				emit(map[string]any{
					"event": "progress",
					"index": idx,
					"file":  name,
					"phase": phase,
					"rows":  n,
				})
			}
			res, err := imp.ImportReader(r.Context(), fh.Filename, f)
			f.Close()
			if err != nil {
				if firstErr == "" {
					firstErr = fh.Filename + ": " + err.Error()
				}
				emit(map[string]any{"event": "error", "file": fh.Filename, "error": err.Error()})
				continue
			}
			results = append(results, res)
			emit(map[string]any{"event": "file_done", "index": i, "result": res})
		}
		// If at least one file succeeded, still emit `done` with the
		// successful slice so the UI can display partial results
		// alongside the error. Pure-failure imports surface only the
		// per-file `error` events the client already knows how to
		// render.
		if firstErr != "" && len(results) == 0 {
			emit(map[string]any{"event": "error", "error": firstErr})
			return
		}
		emit(map[string]any{"event": "done", "files": results, "error": firstErr})
	}
}

// handleDataBackup streams a single JSON bundle containing every
// drive, charge, and raw sample for the current user. Intended to
// be paired with the reset endpoint so a user can snapshot
// their data before wiping it. The response is served with a
// Content-Disposition attachment so browsers download it directly;
// nothing is kept server-side.
//
// It is streamed: the envelope is written by hand and drives, charges and
// samples are encoded one row at a time from a database cursor, so memory
// stays flat however much data the user has (the old version built a slice
// of every sample first and OOM-killed small pods). A guard keeps a few
// backups from running at once and one user from repeating them.
func handleDataBackup(d Deps) http.HandlerFunc {
	guard := guardFor(d, backupMaxConcurrent, "last_backup_at")
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		// A full copy of a user's data is not something an admin viewing as them
		// may take: view-as is a GET-only read of what the user sees, and this is a
		// GET that hands over everything.
		if auth.IsImpersonating(r.Context()) {
			http.Error(w, "not available while viewing as another user", http.StatusForbidden)
			return
		}
		ds := d.Drives.For(uid)
		cs := d.Charges.For(uid)
		ss := d.Samples.For(uid)
		if ds == nil || cs == nil || ss == nil {
			http.Error(w, "backup unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		release, retryAfter, allowed := guard.acquire(r.Context(), uid, backupPerUserGap)
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			http.Error(w, "a backup is already running or was just made; try again shortly", http.StatusTooManyRequests)
			return
		}
		defer release()
		ctx := r.Context()
		// User settings (charging cost, currency, home location,
		// trip planner defaults, drive mode, display prefs, etc.).
		// Flat key/value map — survives schema changes because the
		// store itself is schemaless.
		var userSettings map[string]string
		if ss2 := d.Settings.For(uid); ss2 != nil {
			if m, serr := ss2.GetAll(ctx); serr == nil {
				userSettings = m
			}
		}
		if userSettings == nil {
			userSettings = map[string]string{}
		}
		// Per-vehicle profile (pack capacity, tire placard PSI,
		// wheel size, accessories, frequently-tows flag). The
		// vehicle row itself includes its rivian_vehicle_id so a
		// future restore can match it back to the right truck.
		type profileEntry struct {
			RivianVehicleID string              `json:"rivian_vehicle_id"`
			DisplayName     string              `json:"display_name,omitempty"`
			Profile         db.VehicleProfile   `json:"profile"`
			Lease           *vehiclelease.Terms `json:"lease,omitempty"`
		}
		profiles := []profileEntry{}
		if d.DB != nil {
			vehs, verr := db.ListUserVehicles(ctx, d.DB, uid)
			if verr == nil {
				for _, v := range vehs {
					p, perr := db.GetVehicleProfile(ctx, d.DB, uid, v.ID)
					if perr != nil {
						// best-effort: skip vehicles whose profile
						// can't be read rather than failing the whole
						// backup over one bad row.
						continue
					}
					lease, _ := vehiclelease.NewStore(d.DB).Get(ctx, uid, v.ID)
					profiles = append(profiles, profileEntry{
						RivianVehicleID: v.RivianVehicleID,
						DisplayName:     v.DisplayName,
						Profile:         p,
						Lease:           lease,
					})
				}
			}
		}
		stamp := time.Now().UTC().Format("20060102-150405")
		// Gzip by default: telemetry JSON compresses about tenfold, which keeps
		// a big account's download (and its later upload) small. ?gzip=0 gives
		// the plain .json. Served as application/gzip, not Content-Encoding,
		// so a browser saves the .json.gz instead of unpacking it.
		useGzip := r.URL.Query().Get("gzip") != "0"
		name, ctype := "rivolt-backup-"+stamp+".json", "application/json"
		if useGzip {
			name, ctype = name+".gz", "application/gzip"
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		var out io.Writer = w
		var gz *gzip.Writer
		if useGzip {
			gz, _ = gzip.NewWriterLevel(w, gzip.BestSpeed)
			out = gz
		}
		bw := bufio.NewWriterSize(out, 64<<10)
		flusher, _ := w.(http.Flusher)
		// The server's write deadline (5 min) would cut a big backup on a slow
		// link. Move it forward at every flush: a client that keeps receiving
		// is never cut, one that stalls for streamIdle is.
		rc := http.NewResponseController(w)
		// flushOut pushes buffered bytes (through the compressor) to the client.
		flushOut := func() error {
			_ = rc.SetWriteDeadline(time.Now().Add(streamIdle))
			if err := bw.Flush(); err != nil {
				return err
			}
			if gz != nil {
				if err := gz.Flush(); err != nil {
					return err
				}
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		}
		enc := json.NewEncoder(bw)
		// fail ends a stream that broke after the status line went out: a
		// clean ending would hand the user a truncated file that looks
		// complete, so abort the connection and let the download fail.
		fail := func(what string, err error) {
			slog.WarnContext(ctx, "backup stream failed", "stage", what, "err", err.Error())
			guard.forget(uid) // let the user retry at once
			panic(http.ErrAbortHandler)
		}
		put := func(v any) {
			if err := enc.Encode(v); err != nil {
				fail("encode", err)
			}
		}
		writeStr := func(str string) {
			if _, err := bw.WriteString(str); err != nil {
				fail("write", err)
			}
		}
		flush := func() {
			if err := flushOut(); err != nil {
				fail("flush", err)
			}
			if gz != nil {
				if err := gz.Close(); err != nil {
					fail("gzip", err)
				}
			}
		}
		// streamArray writes "name":[ ... ] one element at a time.
		streamArray := func(name string, each func(emit func(any) error) error) {
			writeStr(`,"` + name + `":[`)
			n := 0
			err := each(func(v any) error {
				if err := ctx.Err(); err != nil { // client went away
					return err
				}
				if n > 0 {
					if _, err := bw.WriteString(","); err != nil {
						return err
					}
				}
				n++
				if err := enc.Encode(v); err != nil {
					return err
				}
				if n%backupFlushEvery == 0 {
					if err := flushOut(); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				fail(name, err)
			}
			writeStr("]")
		}
		writeStr("{")
		writeStr(`"version":`)
		put(d.Version)
		writeStr(`,"created_at":`)
		put(time.Now().UTC().Format(time.RFC3339))
		writeStr(`,"user_settings":`)
		put(userSettings)
		writeStr(`,"vehicle_profiles":`)
		put(profiles)
		streamArray("drives", func(emit func(any) error) error {
			return ds.EachAll(ctx, func(v drives.Drive) error { return emit(v) })
		})
		streamArray("charges", func(emit func(any) error) error {
			return cs.EachAll(ctx, func(v charges.Charge) error { return emit(v) })
		})
		// Thinned months come from the archive in full detail; a month that
		// couldn't be (no readable object) is listed so the file says so.
		var reduced []string
		streamArray("samples", func(emit func(any) error) error {
			var err error
			reduced, err = ss.EachAllArchived(ctx, d.ArchiveRows, func(v samples.Sample) error { return emit(v) })
			return err
		})
		if len(reduced) > 0 {
			writeStr(`,"samples_reduced_months":`)
			put(reduced)
		}
		writeStr("}\n")
		flush()
	}
}

// Backup limits (see backupGuard).
const (
	backupMaxConcurrent = 2                // backups running at once per pod
	backupPerUserGap    = 10 * time.Minute // minimum gap between one user's backups
	backupFlushEvery    = 500              // elements between flushes to the client
	streamIdle          = 2 * time.Minute  // how long a backup or restore may make no progress
)

// backupGuard bounds the cost of /api/data/backup and /api/data/restore: at
// most max run at once on this pod (a per-pod semaphore, since memory and the
// database connection are per pod) and one user may start one per gap. The
// gap is global when the guard is backed by the database: a conditional
// UPDATE on the users row claims the slot atomically, so two replicas cannot
// both let the same user through.
type backupGuard struct {
	sem  chan struct{}
	gaps gapStore

	// cool holds a short per-user cooldown (in memory, per pod) set after a
	// refused or empty restore. Those give the ten-minute slot back, so
	// without it a user could send 100 MiB of junk back to back, each fully
	// parsed and then refused.
	coolMu sync.Mutex
	cool   map[uuid.UUID]time.Time
}

// gapStore remembers when each user last started the operation.
type gapStore interface {
	// claim takes uid's slot if the last start is older than gap. When it
	// refuses it says how long until a retry could succeed.
	claim(ctx context.Context, uid uuid.UUID, gap time.Duration) (ok bool, retryAfter time.Duration, err error)
	// clear gives the slot back (the operation broke; a retry should not wait).
	clear(ctx context.Context, uid uuid.UUID)
}

// newBackupGuard keeps the gap in memory (per pod): for tests and installs
// without a database.
func newBackupGuard(max int, gap time.Duration, now func() time.Time) *backupGuard {
	return &backupGuard{sem: make(chan struct{}, max), gaps: &memGaps{now: now, last: map[uuid.UUID]time.Time{}}}
}

// newDBBackupGuard keeps the gap in users.<column> (last_backup_at or
// last_restore_at), shared by every replica.
func newDBBackupGuard(max int, pool *sql.DB, column string) *backupGuard {
	return &backupGuard{sem: make(chan struct{}, max), gaps: &pgGaps{db: pool, column: column}}
}

// acquire reserves a slot for uid. On refusal it returns how long until a
// retry could succeed. release must be called when the operation ends.
func (g *backupGuard) acquire(ctx context.Context, uid uuid.UUID, gap time.Duration) (release func(), retryAfter time.Duration, ok bool) {
	if wait := g.coolingDown(uid); wait > 0 {
		return nil, wait, false
	}
	select {
	case g.sem <- struct{}{}:
	default:
		return nil, 30 * time.Second, false
	}
	free := func() { <-g.sem }
	claimed, wait, err := g.gaps.claim(ctx, uid, gap)
	if err != nil {
		// This guard is cost control, not security: if the database cannot
		// answer, the per-pod semaphore still bounds the load. Let it through.
		slog.WarnContext(ctx, "backup guard: gap check failed, allowing", "err", logging.ScrubErr(err))
		return free, 0, true
	}
	if !claimed {
		free()
		return nil, wait, false
	}
	return free, 0, true
}

// coolingDown is how long uid must still wait after a refused restore.
func (g *backupGuard) coolingDown(uid uuid.UUID) time.Duration {
	g.coolMu.Lock()
	defer g.coolMu.Unlock()
	until, ok := g.cool[uid]
	if !ok {
		return 0
	}
	if d := time.Until(until); d > 0 {
		return d
	}
	delete(g.cool, uid)
	return 0
}

// forgetWithCooldown gives uid's ten-minute slot back (nothing useful was
// written) but makes the next attempt wait restoreRefusalCooldown.
func (g *backupGuard) forgetWithCooldown(uid uuid.UUID) {
	g.forget(uid)
	g.coolMu.Lock()
	defer g.coolMu.Unlock()
	if g.cool == nil {
		g.cool = map[uuid.UUID]time.Time{}
	}
	now := time.Now()
	for k, t := range g.cool { // forget expired entries
		if now.After(t) {
			delete(g.cool, k)
		}
	}
	g.cool[uid] = now.Add(restoreRefusalCooldown)
}

// forget gives uid's slot back (a run that broke should not cost the user
// the wait).
func (g *backupGuard) forget(uid uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g.gaps.clear(ctx, uid)
}

type memGaps struct {
	now  func() time.Time
	mu   sync.Mutex
	last map[uuid.UUID]time.Time
}

func (m *memGaps) claim(_ context.Context, uid uuid.UUID, gap time.Duration) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if t, seen := m.last[uid]; seen && now.Sub(t) < gap {
		return false, gap - now.Sub(t), nil
	}
	// Forget old entries so the map does not grow without bound.
	for k, t := range m.last {
		if now.Sub(t) >= gap {
			delete(m.last, k)
		}
	}
	m.last[uid] = now
	return true, 0, nil
}

func (m *memGaps) clear(_ context.Context, uid uuid.UUID) {
	m.mu.Lock()
	delete(m.last, uid)
	m.mu.Unlock()
}

// pgGaps stores the gap on the users row. column is one of the two known
// names (never user input).
type pgGaps struct {
	db     *sql.DB
	column string
}

func (p *pgGaps) claim(ctx context.Context, uid uuid.UUID, gap time.Duration) (bool, time.Duration, error) {
	interval := fmt.Sprintf("%d milliseconds", gap.Milliseconds())
	res, err := p.db.ExecContext(ctx, `UPDATE users SET `+p.column+` = now()
		WHERE id = $1 AND (`+p.column+` IS NULL OR `+p.column+` < now() - $2::interval)`, uid, interval)
	if err != nil {
		return false, 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, 0, nil
	}
	// Refused (or no such user): how long is left?
	var left float64
	err = p.db.QueryRowContext(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM (`+p.column+` + $2::interval - now())), 0) FROM users WHERE id = $1`, uid, interval).Scan(&left)
	if err != nil {
		return false, 0, err
	}
	if left < 1 {
		left = 1
	}
	return false, time.Duration(left * float64(time.Second)), nil
}

func (p *pgGaps) clear(ctx context.Context, uid uuid.UUID) {
	if _, err := p.db.ExecContext(ctx, `UPDATE users SET `+p.column+` = NULL WHERE id = $1`, uid); err != nil {
		slog.WarnContext(ctx, "backup guard: could not clear the gap", "err", logging.ScrubErr(err))
	}
}

// guardFor builds the guard for one endpoint: database-backed (global across
// replicas) when there is a database, in memory otherwise.
func guardFor(d Deps, max int, column string) *backupGuard {
	if d.DB != nil {
		return newDBBackupGuard(max, d.DB, column)
	}
	return newBackupGuard(max, backupPerUserGap, time.Now)
}

// handleDataRestore accepts a previously downloaded backup bundle
// (see handleDataBackup), plain JSON or gzip, and upserts every
// drive/charge/sample into the current user's stores. Existing rows with the
// same external_id (drives/charges) or (vehicle_id, at) (samples) are left
// as-is for samples and overwritten for drives/charges — this matches the
// importer's own behavior, so re-running is idempotent.
//
// It is streamed: the body is decoded token by token and samples go in
// restoreBatch at a time, so memory stays flat however large the backup is
// (a heavy account's backup is hundreds of MB; the old version decoded the
// whole file into slices). Batches commit independently, which is safe
// because every write is idempotent: a failed restore can simply be run
// again. Limits: one restore at a time per pod, one per user per ten
// minutes, and a cap on the DECOMPRESSED size (a zip-bomb guard).
func handleDataRestore(d Deps) http.HandlerFunc {
	guard := guardFor(d, 1, "last_restore_at")
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ds := d.Drives.For(uid)
		cs := d.Charges.For(uid)
		ss := d.Samples.For(uid)
		if ds == nil || cs == nil || ss == nil {
			http.Error(w, "restore unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		release, retryAfter, allowed := guard.acquire(r.Context(), uid, backupPerUserGap)
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			http.Error(w, "a restore is already running or was just made; try again shortly", http.StatusTooManyRequests)
			return
		}
		defer release()
		// A big restore outlives the server's default read/write deadlines
		// (5 min). Move them forward as the work progresses (tick is called
		// every few hundred rows), up to restoreDeadline overall.
		rc := http.NewResponseController(w)
		hardStop := time.Now().Add(restoreDeadline)
		tick := func() {
			d := time.Now().Add(streamIdle)
			if d.After(hardStop) {
				d = hardStop
			}
			_ = rc.SetReadDeadline(d)
			_ = rc.SetWriteDeadline(d)
		}
		tick()

		src, closeSrc, err := restoreSource(http.MaxBytesReader(w, r.Body, restoreMaxWire), restoreMaxPlain)
		if err != nil {
			http.Error(w, msgDamaged, http.StatusBadRequest)
			guard.forgetWithCooldown(uid)
			return
		}
		defer closeSrc()
		gate := newRestoreGate(time.Now())
		if d.SampleRetentionMonths > 0 {
			gate.sampleFrom = samples.RetentionCutoff(time.Now(), d.SampleRetentionMonths)
		}
		if d.DB != nil {
			if vs, verr := db.ListUserVehicles(r.Context(), d.DB, uid); verr == nil {
				for _, v := range vs {
					gate.owned[v.RivianVehicleID] = true
				}
			}
		}
		// Answer at once and report progress as NDJSON lines: a long restore
		// would otherwise sit silent past Cloudflare's ~100 s limit. The
		// request body is still being read, so full duplex is needed on
		// HTTP/1.
		_ = rc.EnableFullDuplex()
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		var wmu sync.Mutex
		emit := func(m map[string]any) {
			wmu.Lock()
			defer wmu.Unlock()
			_ = json.NewEncoder(w).Encode(m)
			_ = rc.Flush()
		}
		prog := &restoreProgress{}
		counts := func(event string) map[string]any {
			m := map[string]any{"event": event, "drives": prog.drives.Load(), "charges": prog.charges.Load(), "samples": prog.samples.Load()}
			if n := gate.skipped.Load(); n > 0 {
				m["skipped"] = n
			}
			return m
		}
		emit(map[string]any{"event": "start"})
		stopBeat := make(chan struct{})
		beatDone := make(chan struct{})
		go func() { // a line every few seconds, so the connection never goes quiet
			defer close(beatDone)
			t := time.NewTicker(restoreHeartbeat)
			defer t.Stop()
			for {
				select {
				case <-stopBeat:
					return
				case <-t.C:
					emit(counts("progress"))
				}
			}
		}()
		_, err = restoreStream(r.Context(), json.NewDecoder(src), ds, cs, ss, tick, gate, prog)
		close(stopBeat)
		<-beatDone
		if err != nil {
			status, msg := restoreErrorEvent(err)
			if status == http.StatusInternalServerError {
				guard.forget(uid) // our failure: an idempotent retry should not wait
			} else {
				guard.forgetWithCooldown(uid) // a refused file: free the slot, but not for back-to-back junk
			}
			if status == http.StatusInternalServerError {
				slog.WarnContext(r.Context(), "restore failed",
					"user", logging.IDHash(uid.String()), "err", logging.ScrubErr(err))
			}
			m := counts("error")
			m["status"], m["error"] = status, msg
			emit(m)
			return
		}
		if prog.drives.Load()+prog.charges.Load()+prog.samples.Load() == 0 {
			guard.forgetWithCooldown(uid) // nothing was written: no ten-minute wait, but not back to back
		}
		emit(counts("done"))
	}
}

// restoreReadErr classifies an error from reading or decoding the body: a
// network or deadline failure, a size limit or a cancelled request keeps its
// identity (the client then gets the generic message, never the text, which
// can carry internal addresses); anything else is a malformed backup.
func restoreReadErr(ctx context.Context, err error) error {
	var tooBig *http.MaxBytesError
	var netErr net.Error
	switch {
	case errors.Is(err, errRestoreTooBig), errors.As(err, &tooBig),
		errors.As(err, &netErr), errors.Is(err, os.ErrDeadlineExceeded),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		ctx.Err() != nil:
		return err
	}
	return fmt.Errorf("%w: %v", errRestoreBadFile, err)
}

// What the client is told about a file we can't read. The decoder's own text
// ("invalid character 'h' in literal true", "unexpected EOF") stays out of it.
const (
	msgNotBackup = "That file isn't a Rivolt backup."
	msgDamaged   = "That file isn't a Rivolt backup, or it's damaged."
)

// restoreErrorEvent maps a restore failure to the status and message the
// client sees. Only the failures we expect carry their own text; anything else
// (a database error, a partition failure) is generic, and the caller logs it.
func restoreErrorEvent(err error) (int, string) {
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig), errors.Is(err, errRestoreTooBig):
		return http.StatusRequestEntityTooLarge, "backup is too large"
	case errors.Is(err, errRestoreNotBackup):
		return http.StatusBadRequest, msgNotBackup
	case errors.Is(err, errRestoreBadFile):
		return http.StatusBadRequest, msgDamaged
	case errors.Is(err, errRestoreVehicles):
		return http.StatusBadRequest, err.Error()
	}
	return http.StatusInternalServerError, "The restore stopped partway. It may be partly restored; running it again is safe."
}

// restoreRefusalCooldown is the wait after a refused or empty restore (a
// variable so a test can shorten it).
var restoreRefusalCooldown = 30 * time.Second

// Restore limits.
const (
	restoreBatch    = 2000             // samples per insert batch
	restoreMaxWire  = 100 << 20        // bytes accepted on the wire (Cloudflare's upload limit); the gzip size, not the unpacked one
	restoreDeadline = 30 * time.Minute // read/write deadline for one restore
)

// restoreMaxPlain caps the decompressed size of a backup (a variable so a
// test can lower it).
var restoreMaxPlain int64 = 2 << 30

// restoreHeartbeat is how often a restore reports progress (a variable so a
// test can shorten it).
var restoreHeartbeat = 3 * time.Second

var (
	errRestoreTooBig  = errors.New("backup is too large when decompressed")
	errRestoreBadFile = errors.New("not a Rivolt backup")
	// errRestoreNotBackup: valid JSON that holds none of a backup's arrays.
	errRestoreNotBackup = errors.New("no drives, charges or samples in the file")
)

// restoreSource returns the reader to decode: the body itself, or its gzip
// unpacking when it starts with the gzip magic bytes, capped at maxPlain
// decompressed bytes.
func restoreSource(body io.Reader, maxPlain int64) (io.Reader, func(), error) {
	br := bufio.NewReaderSize(body, 64<<10)
	closeFn := func() {}
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid gzip: %w", err)
		}
		closeFn = func() { _ = gz.Close() }
		src = gz
	}
	return &capReader{r: src, left: maxPlain}, closeFn, nil
}

// capReader fails with errRestoreTooBig once more than left bytes were read.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errRestoreTooBig
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// restoreProgress counts what a running restore has written; the heartbeat
// reads it while the decoder updates it.
type restoreProgress struct {
	drives, charges, samples atomic.Int64
}

type restoreResult struct {
	Drives  int `json:"drives"`
	Charges int `json:"charges"`
	Samples int `json:"samples"`
	// Skipped counts rows left out because their time is outside the
	// accepted range (a real backup has none).
	Skipped int `json:"skipped,omitempty"`
	// SamplesPastRetention counts samples left out because they are older
	// than the install keeps raw samples (RIVOLT_SAMPLE_RETENTION_MONTHS);
	// their drives and charges are restored.
	SamplesPastRetention int `json:"samples_past_retention,omitempty"`
}

// A restore file is untrusted input: signups are instant, and every distinct
// month of samples creates a vehicle_state partition (which slows query
// planning for everyone) while every unknown Rivian id creates a vehicle. So
// rows outside a sane time window are skipped before anything is created, and
// a restore may add only a few vehicles.
var (
	restoreMinTime        = time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	restoreFutureSlack    = 24 * time.Hour
	restoreMaxNewVehicles = 5
	errRestoreVehicles    = errors.New("the backup refers to too many vehicles that are not on this account")
)

type restoreGate struct {
	from, to time.Time
	owned    map[string]bool // Rivian ids already on the account
	fresh    map[string]bool // ids this restore would create
	skipped  atomic.Int64
	// sampleFrom is the sample retention cutoff (zero: keep all); older
	// samples are counted in pastRetention and left out.
	sampleFrom    time.Time
	pastRetention atomic.Int64
}

func newRestoreGate(now time.Time) *restoreGate {
	return &restoreGate{from: restoreMinTime, to: now.Add(restoreFutureSlack), owned: map[string]bool{}, fresh: map[string]bool{}}
}

// timesOK reports whether every time is inside [2021-01-01, now+24h]; a row
// that is not is counted and skipped.
func (g *restoreGate) timesOK(ts ...time.Time) bool {
	for _, t := range ts {
		if t.Before(g.from) || t.After(g.to) {
			g.skipped.Add(1)
			return false
		}
	}
	return true
}

// sampleOK is timesOK for a sample, which must also be inside the install's
// sample retention: an older one would recreate a dropped partition only for
// the next sweep to delete it.
func (g *restoreGate) sampleOK(t time.Time) bool {
	if !g.timesOK(t) {
		return false
	}
	if !g.sampleFrom.IsZero() && t.Before(g.sampleFrom) {
		g.pastRetention.Add(1)
		return false
	}
	return true
}

// vehicleOK admits a row's vehicle: one the account has, or one of at most
// restoreMaxNewVehicles new ids.
func (g *restoreGate) vehicleOK(id string) error {
	if g.owned[id] || g.fresh[id] {
		return nil
	}
	if len(g.fresh) >= restoreMaxNewVehicles {
		return fmt.Errorf("%w (more than %d)", errRestoreVehicles, restoreMaxNewVehicles)
	}
	g.fresh[id] = true
	return nil
}

// restoreStream decodes the bundle token by token. Keys may come in any
// order (older backups list charges first); unknown keys are skipped.
func restoreStream(ctx context.Context, dec *json.Decoder, ds *drives.Store, cs *charges.Store, ss *samples.Store, tick func(), gate *restoreGate, prog *restoreProgress) (restoreResult, error) {
	var res restoreResult
	bad := func(err error) error { return fmt.Errorf("%w: %v", errRestoreBadFile, err) }
	wrap := func(err error) error { return restoreReadErr(ctx, err) }
	if tok, err := dec.Token(); err != nil {
		return res, wrap(err)
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return res, bad(errors.New("expected a JSON object"))
	}
	array := func(each func() error) error {
		if tok, err := dec.Token(); err != nil {
			return wrap(err)
		} else if tok == nil { // null: an old backup with no rows
			return nil
		} else if d, ok := tok.(json.Delim); !ok || d != '[' {
			return bad(errors.New("expected an array"))
		}
		for rows := 0; dec.More(); rows++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if rows%200 == 0 {
				tick()
			}
			if err := each(); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return wrap(err)
		}
		return nil
	}
	sawArray := false // a backup holds at least one of drives, charges, samples
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return res, wrap(err)
		}
		key, _ := keyTok.(string)
		if key == "drives" || key == "charges" || key == "samples" {
			sawArray = true
		}
		switch key {
		case "drives":
			err = array(func() error {
				var v drives.Drive
				if err := dec.Decode(&v); err != nil {
					return wrap(err)
				}
				if !gate.timesOK(v.StartedAt, v.EndedAt) {
					return nil
				}
				if err := gate.vehicleOK(v.VehicleID); err != nil {
					return err
				}
				if err := ds.Upsert(ctx, v); err != nil {
					return fmt.Errorf("upsert drive %s: %w", v.ID, err)
				}
				prog.drives.Add(1)
				return nil
			})
		case "charges":
			err = array(func() error {
				var v charges.Charge
				if err := dec.Decode(&v); err != nil {
					return wrap(err)
				}
				if !gate.timesOK(v.StartedAt, v.EndedAt) {
					return nil
				}
				if err := gate.vehicleOK(v.VehicleID); err != nil {
					return err
				}
				if err := cs.Upsert(ctx, v); err != nil {
					return fmt.Errorf("upsert charge %s: %w", v.ID, err)
				}
				prog.charges.Add(1)
				return nil
			})
		case "samples":
			batch := make([]samples.Sample, 0, restoreBatch)
			flush := func() error {
				if len(batch) == 0 {
					return nil
				}
				if err := ss.InsertBulk(ctx, batch); err != nil {
					return fmt.Errorf("insert samples: %w", err)
				}
				prog.samples.Add(int64(len(batch)))
				batch = batch[:0]
				return nil
			}
			err = array(func() error {
				var v samples.Sample
				if err := dec.Decode(&v); err != nil {
					return wrap(err)
				}
				if !gate.sampleOK(v.At) {
					return nil
				}
				if err := gate.vehicleOK(v.VehicleID); err != nil {
					return err
				}
				batch = append(batch, v)
				if len(batch) == restoreBatch {
					return flush()
				}
				return nil
			})
			if err == nil {
				err = flush()
			}
		default: // version, created_at, user_settings, vehicle_profiles, anything new
			var skip json.RawMessage
			err = dec.Decode(&skip)
			if err != nil {
				err = wrap(err)
			}
		}
		if err != nil {
			return res, err
		}
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return res, wrap(err)
	}
	if !sawArray {
		return res, errRestoreNotBackup
	}
	res = restoreResult{Drives: int(prog.drives.Load()), Charges: int(prog.charges.Load()), Samples: int(prog.samples.Load()), Skipped: int(gate.skipped.Load()),
		SamplesPastRetention: int(gate.pastRetention.Load())}
	return res, nil
}

// handleDataReset truncates the three session tables for the current
// user (drives, charges, vehicle_state). Vehicles, user_settings,
// push_subscriptions, and the user row are preserved so settings
// and the Rivian account link survive. Returns deleted row counts.
//
// This is the UI counterpart to what used to be a psql TRUNCATE.
// Pair with /data/backup to avoid losing work.
// accountWriteTimeout bounds an account deletion or history reset once it has
// started. They wait on the user's telemetry-archive lock, which an archive
// write holds through its COPY, PUT and verify GET (worst case an object at
// the 256 MB cap, a few minutes against the NAS), so they run past the
// request: Cloudflare gives up on the origin after ~100 s and the browser may
// close, and a deletion cut off there would roll back.
const accountWriteTimeout = 5 * time.Minute

// accountWriteContext is the request's context without its cancellation,
// bounded by accountWriteTimeout.
func accountWriteContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), accountWriteTimeout)
}

func handleDataReset(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ds := d.Drives.For(uid)
		cs := d.Charges.For(uid)
		ss := d.Samples.For(uid)
		if ds == nil || cs == nil || ss == nil {
			http.Error(w, "reset unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := accountWriteContext(r)
		defer cancel()
		// Samples first, then the telemetry archive. The other way round, an
		// archive run between the two could archive the samples about to go,
		// under the user's new key and after the deletion request, and they
		// would outlive the reset. With the samples gone first, a run that
		// already had them finishes under the archive lock before
		// ResetUserArchive, whose deletion then covers its object. A failure
		// leaves a retry, which is idempotent.
		samplesN, err := ss.Reset(ctx)
		if err != nil {
			http.Error(w, "reset samples: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// The archive key goes at once (the objects are unreadable from here
		// on) and the objects are queued for deletion.
		if err := db.ResetUserArchive(ctx, d.DB, uid); err != nil {
			http.Error(w, "reset archive: "+err.Error(), http.StatusInternalServerError)
			return
		}
		drivesN, err := ds.Reset(ctx)
		if err != nil {
			http.Error(w, "reset drives: "+err.Error(), http.StatusInternalServerError)
			return
		}
		chargesN, err := cs.Reset(ctx)
		if err != nil {
			http.Error(w, "reset charges: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"drives":  drivesN,
			"charges": chargesN,
			"samples": samplesN,
		})
	}
}

// handleDataAccountDelete is the self-service counterpart to the
// admin user-delete endpoint: a signed-in user can purge their own
// account end-to-end. Mirrors the admin path's safety rails:
//
//   - "last admin" guard so the install never ends up with zero
//     admins by self-service (the user has to ask another admin to
//     promote a successor first).
//   - resolve the IdP username BEFORE the cascade delete because
//     the rivolt users row is gone afterward.
//
// Cascade order: rivolt DB delete (cascades through every FK'd
// per-user table — drives, charges, samples, settings,
// user_secrets, push_subscriptions, sessions, ...), then Kratos /
// IdP delete, then clear the auth cookie. The next request from
// the same browser is unauthenticated.
func handleDataAccountDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		// Last-admin guard. An admin can self-delete only if there's
		// at least one other admin — otherwise the install ends up
		// with no admin and recovery requires direct DB access.
		role, rerr := db.RoleFor(r.Context(), d.DB, uid)
		if rerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": rerr.Error()})
			return
		}
		if role == "admin" {
			n, cerr := db.CountAdmins(r.Context(), d.DB)
			if cerr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": cerr.Error()})
				return
			}
			if n <= 1 {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "cannot delete the last admin — promote another user first",
				})
				return
			}
		}
		// Resolve username for IdP cleanup before cascade delete.
		var username string
		if d.Users != nil && d.Users.Enabled() {
			u, uerr := db.RawUsernameByID(r.Context(), d.DB, uid)
			if uerr != nil && d.Logger != nil {
				d.Logger.Warn("self-delete: lookup username for idp delete failed",
					"id", uid.String(), "err", uerr.Error())
			}
			username = u
		}
		// Runs to the end even if the client goes away (see accountWriteContext).
		delCtx, cancel := accountWriteContext(r)
		defer cancel()
		if err := db.DeleteUserAccount(delCtx, d.DB, uid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		// A failed IdP deletion is recorded for the admin page; the
		// account is gone on our side either way.
		_, _ = deleteFromIdP(delCtx, d.DB, d.Users, username, d.Logger)
		// Clear the session cookie so the next request from this
		// browser is unauthenticated. The session row was already
		// cascaded out of `sessions` by DeleteUser; this just stops
		// the now-orphan cookie from showing up in the next request.
		if d.Auth != nil {
			d.Auth.Logout(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true})
	}
}

// --- Charge clustering ----------------------------------------------------
//
// Pure-local classification of charge sessions into Home / Public / Fast.
// Fast is anything peaking >=50 kW (DCFC) regardless of location; the
// rest is DBSCAN-clustered on (lat, lon) with the biggest cluster
// winning Home. The UI uses this for /charges badges and the Overview
// Charging locations card. No external calls.
