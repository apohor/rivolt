package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/electrafi"
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
	guard := newBackupGuard(backupMaxConcurrent, backupPerUserGap, time.Now)
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
			http.Error(w, "backup unavailable: stores not initialized", http.StatusServiceUnavailable)
			return
		}
		release, retryAfter, allowed := guard.acquire(uid)
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
		// flushOut pushes buffered bytes (through the compressor) to the client.
		flushOut := func() error {
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
		streamArray("samples", func(emit func(any) error) error {
			return ss.EachAll(ctx, func(v samples.Sample) error { return emit(v) })
		})
		writeStr("}\n")
		flush()
	}
}

// Backup limits (see backupGuard).
const (
	backupMaxConcurrent = 2                // backups running at once per pod
	backupPerUserGap    = 10 * time.Minute // minimum gap between one user's backups
	backupFlushEvery    = 500              // elements between flushes to the client
)

// backupGuard bounds the cost of /api/data/backup: at most max run at once
// on this pod and one user may start one per gap. In-memory and per pod, so
// the effective per-user rate is gap divided by the number of pods.
type backupGuard struct {
	sem  chan struct{}
	gap  time.Duration
	now  func() time.Time
	mu   sync.Mutex
	last map[uuid.UUID]time.Time
}

func newBackupGuard(max int, gap time.Duration, now func() time.Time) *backupGuard {
	return &backupGuard{sem: make(chan struct{}, max), gap: gap, now: now, last: map[uuid.UUID]time.Time{}}
}

// acquire reserves a slot for uid. On refusal it returns how long until a
// retry could succeed. release must be called when the backup ends.
func (g *backupGuard) acquire(uid uuid.UUID) (release func(), retryAfter time.Duration, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if t, seen := g.last[uid]; seen && now.Sub(t) < g.gap {
		return nil, g.gap - now.Sub(t), false
	}
	select {
	case g.sem <- struct{}{}:
	default:
		return nil, 30 * time.Second, false
	}
	// Forget old entries so the map does not grow without bound.
	for k, t := range g.last {
		if now.Sub(t) >= g.gap {
			delete(g.last, k)
		}
	}
	g.last[uid] = now
	return func() { <-g.sem }, 0, true
}

// forget clears uid's gap (a backup that broke should not cost the user
// the wait).
func (g *backupGuard) forget(uid uuid.UUID) {
	g.mu.Lock()
	delete(g.last, uid)
	g.mu.Unlock()
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
	guard := newBackupGuard(1, backupPerUserGap, time.Now)
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
		release, retryAfter, allowed := guard.acquire(uid)
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			http.Error(w, "a restore is already running or was just made; try again shortly", http.StatusTooManyRequests)
			return
		}
		defer release()
		// A big restore outlives the server's default read/write deadlines.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(restoreDeadline))
		_ = rc.SetWriteDeadline(time.Now().Add(restoreDeadline))

		src, closeSrc, err := restoreSource(http.MaxBytesReader(w, r.Body, restoreMaxWire), restoreMaxPlain)
		if err != nil {
			http.Error(w, "read backup: "+err.Error(), http.StatusBadRequest)
			guard.forget(uid)
			return
		}
		defer closeSrc()
		res, err := restoreStream(r.Context(), json.NewDecoder(src), ds, cs, ss)
		if err != nil {
			guard.forget(uid) // an idempotent retry should not wait
			var tooBig *http.MaxBytesError
			switch {
			case errors.As(err, &tooBig), errors.Is(err, errRestoreTooBig):
				http.Error(w, "backup is too large", http.StatusRequestEntityTooLarge)
			case errors.Is(err, errRestoreBadFile):
				http.Error(w, "parse backup: "+err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// Restore limits.
const (
	restoreBatch    = 2000             // samples per insert batch
	restoreMaxWire  = 1 << 30          // bytes accepted on the wire (compressed or not)
	restoreDeadline = 30 * time.Minute // read/write deadline for one restore
)

// restoreMaxPlain caps the decompressed size of a backup (a variable so a
// test can lower it).
var restoreMaxPlain int64 = 2 << 30

var (
	errRestoreTooBig  = errors.New("backup is too large when decompressed")
	errRestoreBadFile = errors.New("not a Rivolt backup")
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

type restoreResult struct {
	Drives  int `json:"drives"`
	Charges int `json:"charges"`
	Samples int `json:"samples"`
}

// restoreStream decodes the bundle token by token. Keys may come in any
// order (older backups list charges first); unknown keys are skipped.
func restoreStream(ctx context.Context, dec *json.Decoder, ds *drives.Store, cs *charges.Store, ss *samples.Store) (restoreResult, error) {
	var res restoreResult
	bad := func(err error) error { return fmt.Errorf("%w: %v", errRestoreBadFile, err) }
	wrap := func(err error) error { // size errors keep their identity
		var tooBig *http.MaxBytesError
		if errors.Is(err, errRestoreTooBig) || errors.As(err, &tooBig) || ctx.Err() != nil {
			return err
		}
		return bad(err)
	}
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
		for dec.More() {
			if err := ctx.Err(); err != nil {
				return err
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
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return res, wrap(err)
		}
		key, _ := keyTok.(string)
		switch key {
		case "drives":
			err = array(func() error {
				var v drives.Drive
				if err := dec.Decode(&v); err != nil {
					return wrap(err)
				}
				if err := ds.Upsert(ctx, v); err != nil {
					return fmt.Errorf("upsert drive %s: %w", v.ID, err)
				}
				res.Drives++
				return nil
			})
		case "charges":
			err = array(func() error {
				var v charges.Charge
				if err := dec.Decode(&v); err != nil {
					return wrap(err)
				}
				if err := cs.Upsert(ctx, v); err != nil {
					return fmt.Errorf("upsert charge %s: %w", v.ID, err)
				}
				res.Charges++
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
				res.Samples += len(batch)
				batch = batch[:0]
				return nil
			}
			err = array(func() error {
				var v samples.Sample
				if err := dec.Decode(&v); err != nil {
					return wrap(err)
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
	return res, nil
}

// handleDataReset truncates the three session tables for the current
// user (drives, charges, vehicle_state). Vehicles, user_settings,
// push_subscriptions, and the user row are preserved so settings
// and the Rivian account link survive. Returns deleted row counts.
//
// This is the UI counterpart to what used to be a psql TRUNCATE.
// Pair with /data/backup to avoid losing work.
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
		ctx := r.Context()
		// Wipe in an order that can't violate FKs; there are no
		// cross-table FKs on user_id so order is cosmetic.
		samplesN, err := ss.Reset(ctx)
		if err != nil {
			http.Error(w, "reset samples: "+err.Error(), http.StatusInternalServerError)
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
		if err := db.DeleteUserAccount(r.Context(), d.DB, uid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		// A failed IdP deletion is recorded for the admin page; the
		// account is gone on our side either way.
		_, _ = deleteFromIdP(r.Context(), d.DB, d.Users, username, d.Logger)
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
