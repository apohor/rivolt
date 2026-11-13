package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/histfloor"
	"github.com/apohor/rivolt/internal/histimport"
	"github.com/apohor/rivolt/internal/imports"
)

// History import (design: history-import-design.md). A user uploads another app's export, sees what would be added (preview, nothing
// written), then sends the same files again to commit. The files are parsed in memory and never stored. Routes exist only when
// Deps.HistoryImport is set (RIVOLT_HISTORY_IMPORT=1); off, every path here is a 404.
const (
	// importMaxBytes caps one request body: every file together.
	importMaxBytes = 50 << 20
	// importParseSlots is the per-pod limit on requests parsing at once (R8, DA's condition). Derivation: the worst legitimate parse peaks at
	// about 49 MB (a 100k-session CSV) and the worst adversarial file at about 16 MB, on top of the 50 MB upload held in memory; 2 slots
	// keep that under the 400 MiB budget of a 512Mi pod. Raising it, or lowering the pod limit, needs DA's re-ack.
	importParseSlots = 2
	// importPreviewsPerHour and importCommitsPerHour are per user, counted across pods (Redis for previews, the imports table for commits).
	importPreviewsPerHour = 30
	importCommitsPerHour  = 10
	importProgressEvery   = 200
	// importReadWindow bounds how long one request may take to send its body while it holds a parse slot (50 MB in three minutes is
	// 2.2 Mbit/s); importWriteWindow is how far each commit step pushes the connection's write deadline out, because the server's own
	// WriteTimeout (5 min from the request) is shorter than a 100k-session commit.
	importReadWindow  = 3 * time.Minute
	importWriteWindow = 2 * time.Minute
	// importMaxFieldParts and the field names bound the non-file parts of a form: a body of tiny uniquely named fields would otherwise
	// build a map of hundreds of thousands of entries while holding a slot.
	importMaxFieldParts = 16
)

var importFieldNames = map[string]bool{
	"source": true, "vehicle_id": true, "tz": true, "distance_unit": true, "energy_unit": true, "pack_kwh": true, "vehicle": true, "sha256": true,
}

// importSources maps the form's source name to its formats, tried in order for each file.
var importSources = map[string][]histimport.Format{
	"tezlab": {histimport.TezLabDrives, histimport.TezLabCharges},
	"abrp":   {histimport.ABRPPoints},
}

type historyImportAPI struct {
	d    Deps
	sem  chan struct{}
	now  func() time.Time
	logr *slog.Logger

	mu     sync.Mutex
	active map[uuid.UUID]bool // users with an import request in flight on this pod
}

func newHistoryImportAPI(d Deps) *historyImportAPI {
	l := d.Logger
	if l == nil {
		l = slog.Default()
	}
	return &historyImportAPI{d: d, sem: make(chan struct{}, importParseSlots), now: time.Now, logr: l, active: map[uuid.UUID]bool{}}
}

// mount registers the routes in the bulk-data group (no 30 s timeout: a commit streams for as long as it writes).
func (a *historyImportAPI) mount(r chi.Router, ls *limiterSet) {
	pv := ls.ip("import-preview", importPreviewsPerHour, time.Hour, 2*time.Hour)
	pv.key = userKey
	r.With(pv.Middleware).Post("/import/preview", a.preview)
	r.Post("/import/commit", a.commit)
	r.Get("/imports", a.list)
	r.Post("/imports/{id}/undo", a.undo)
}

func userKey(r *http.Request) string {
	if uid, ok := auth.UserFromContext(r.Context()); ok {
		return uid.String()
	}
	return clientIP(r)
}

// acquire takes a parse slot or answers 503 Retry-After 10.
func (a *historyImportAPI) acquire(w http.ResponseWriter) bool {
	select {
	case a.sem <- struct{}{}:
		return true
	default:
		if a.d.Metrics != nil {
			a.d.Metrics.ImportParseRejected.Inc()
		}
		w.Header().Set("Retry-After", "10")
		importError(w, http.StatusServiceUnavailable, "busy", "Importing is busy right now. Try again in a few seconds.")
		return false
	}
}

func (a *historyImportAPI) release() { <-a.sem }

// sameSite refuses a request that did not come from our own origin. A multipart POST is a simple cross-origin request and the session
// cookie is SameSite=Lax on the whole site, so a page on another subdomain could post here (and undo deletes rows). Browsers say where a
// request comes from; anything but our own origin (or no page at all) is refused.
func sameSite(w http.ResponseWriter, r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		importError(w, http.StatusForbidden, "cross_site_request", "This request did not come from rivolt.dev.")
		return false
	}
	return true
}

// begin admits one request: a user has one import in flight per pod (a slow upload cannot hold both slots), then a parse slot. It returns
// the function that gives both back.
func (a *historyImportAPI) begin(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if !sameSite(w, r) {
		return nil, false
	}
	uid, _ := auth.UserFromContext(r.Context())
	if uid != uuid.Nil {
		a.mu.Lock()
		busy := a.active[uid]
		if !busy {
			a.active[uid] = true
		}
		a.mu.Unlock()
		if busy {
			w.Header().Set("Retry-After", "10")
			importError(w, http.StatusTooManyRequests, "in_progress", "Another import of yours is still running. Wait for it to finish.")
			return nil, false
		}
	}
	if !a.acquire(w) {
		a.forget(uid)
		return nil, false
	}
	return func() { a.release(); a.forget(uid) }, true
}

func (a *historyImportAPI) forget(uid uuid.UUID) {
	if uid == uuid.Nil {
		return
	}
	a.mu.Lock()
	delete(a.active, uid)
	a.mu.Unlock()
}

func importError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": msg, "code": code})
}

// importRequest is one upload, read.
type importRequest struct {
	uid      uuid.UUID
	rivianID string
	source   string
	formats  []histimport.Format
	opts     histimport.Options
	tz       string
	files    []importFile
	sha      string // the combined hash of the files
	wantSHA  string // what the client says the preview saw (commit only)
}

type importFile struct {
	name string // only the extension is ever used
	data []byte
}

func (f importFile) ext() string {
	if e := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.name), ".")); e == "xlsx" {
		return "xlsx"
	}
	return "csv"
}

// read parses the multipart form into memory. A part is a field (small) or a file (counted against the one body cap).
func (a *historyImportAPI) read(w http.ResponseWriter, r *http.Request) (*importRequest, bool) {
	uid, ok := auth.UserFromContext(r.Context())
	if !ok || uid == uuid.Nil {
		importError(w, http.StatusUnauthorized, "unauthenticated", "Sign in first.")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, importMaxBytes)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(a.now().Add(importReadWindow)) // not supported on a test recorder
	mr, err := r.MultipartReader()
	if err != nil {
		importError(w, http.StatusBadRequest, "bad_upload", "Send the files as a multipart form.")
		return nil, false
	}
	req := &importRequest{uid: uid}
	fields := map[string]string{}
	nFields := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			a.readFailed(w, err)
			return nil, false
		}
		if part.FileName() == "" {
			if nFields++; !importFieldNames[part.FormName()] || nFields > importMaxFieldParts {
				importError(w, http.StatusBadRequest, "bad_upload", "The form has a field we do not know.")
				return nil, false
			}
			b, err := io.ReadAll(io.LimitReader(part, 1<<10))
			if err != nil {
				a.readFailed(w, err)
				return nil, false
			}
			fields[part.FormName()] = strings.TrimSpace(string(b))
			continue
		}
		if part.FormName() != "file" || len(req.files) >= 4 {
			importError(w, http.StatusBadRequest, "bad_upload", "Send up to four files under the field 'file'.")
			return nil, false
		}
		var buf bytes.Buffer
		// One allocation for the common single-file upload instead of ReadAll's doubling up to 50 MB.
		if n := r.ContentLength; n > 0 && n <= importMaxBytes && len(req.files) == 0 {
			buf.Grow(int(n))
		}
		if _, err := buf.ReadFrom(part); err != nil {
			a.readFailed(w, err)
			return nil, false
		}
		req.files = append(req.files, importFile{name: part.FileName(), data: buf.Bytes()})
	}
	if len(req.files) == 0 {
		importError(w, http.StatusBadRequest, "no_file", "Choose a file to import.")
		return nil, false
	}
	req.source = fields["source"]
	req.formats = importSources[req.source]
	if req.formats == nil {
		importError(w, http.StatusBadRequest, "bad_source", "Choose TezLab or ABRP.")
		return nil, false
	}
	req.rivianID = fields["vehicle_id"]
	if req.rivianID == "" {
		importError(w, http.StatusBadRequest, "no_vehicle", "Choose the vehicle the history belongs to.")
		return nil, false
	}
	owns, err := db.OwnsRivianID(r.Context(), a.d.DB, uid, req.rivianID)
	if err != nil {
		serverError(w, r, "history import", err)
		return nil, false
	}
	if !owns {
		// 404, not 403: a probe cannot learn whether a vehicle id exists.
		importError(w, http.StatusNotFound, "vehicle_not_found", "Vehicle not found.")
		return nil, false
	}
	req.tz = fields["tz"]
	if req.tz != "" {
		loc, err := time.LoadLocation(req.tz)
		if err != nil {
			importError(w, http.StatusBadRequest, "bad_tz", "That time zone is not known.")
			return nil, false
		}
		req.opts.Location = loc
	}
	switch u := fields["distance_unit"]; u {
	case "", "km", "mi":
		req.opts.DistanceUnit = u
	default:
		importError(w, http.StatusBadRequest, "bad_unit", "Distance unit must be km or mi.")
		return nil, false
	}
	switch u := fields["energy_unit"]; u {
	case "", "kwh", "wh":
		req.opts.EnergyUnit = u
	default:
		importError(w, http.StatusBadRequest, "bad_unit", "Energy unit must be kwh or wh.")
		return nil, false
	}
	if v := fields["pack_kwh"]; v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f > 400 {
			importError(w, http.StatusBadRequest, "bad_pack", "Pack size must be a number of kWh.")
			return nil, false
		}
		req.opts.PackKWh = f
	}
	req.opts.Vehicle = fields["vehicle"]
	req.wantSHA = fields["sha256"]
	req.sha = combinedSHA(req.files)
	return req, true
}

func (a *historyImportAPI) readFailed(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		a.refused("unknown", &parseFailure{code: "too_big"})
		importError(w, http.StatusRequestEntityTooLarge, "too_big", "That is more than 50 MB. Export a shorter range.")
		return
	}
	importError(w, http.StatusBadRequest, "bad_upload", "The upload could not be read.")
}

// combinedSHA is the file's own hash for one file, else the hash of the sorted file hashes: the same files in any order agree.
func combinedSHA(files []importFile) string {
	hs := make([]string, len(files))
	for i, f := range files {
		h := sha256.Sum256(f.data)
		hs[i] = hex.EncodeToString(h[:])
	}
	if len(hs) == 1 {
		return hs[0]
	}
	sort.Strings(hs)
	h := sha256.Sum256([]byte(strings.Join(hs, "")))
	return hex.EncodeToString(h[:])
}

// parsed is every file of a request, merged.
type parsed struct {
	drives  []histimport.Drive
	charges []histimport.Charge
	report  histimport.Report
}

// parse reads each file in the first of the source's formats whose columns it has. A file in no format is refused with the header names
// only (never a row value).
func (req *importRequest) parse(now time.Time) (*parsed, *parseFailure) {
	out := &parsed{report: histimport.Report{Skipped: map[string]int{}}}
	opts := req.opts
	opts.Now = now
	for _, f := range req.files {
		var res *histimport.Result
		var missing *histimport.MissingColumnsError
		for _, fm := range req.formats {
			t, err := openTable(f)
			if err != nil {
				return nil, &parseFailure{status: http.StatusUnprocessableEntity, code: "bad_file", msg: "That file could not be read as a CSV or Excel export."}
			}
			res, err = histimport.Parse(t, fm, opts)
			if err == nil {
				break
			}
			res = nil
			var me *histimport.MissingColumnsError
			if errors.As(err, &me) {
				if missing == nil {
					missing = me
				}
				continue
			}
			return nil, failureOf(err)
		}
		if res == nil {
			pf := &parseFailure{status: http.StatusUnprocessableEntity, code: "unknown_columns", expected: histimport.RequiredColumns(req.formats)}
			pf.msg = unknownColumnsMessage(pf.expected)
			if missing != nil {
				pf.header = nameLikeCells(missing.Header)
				pf.cells = nonEmpty(missing.Header)
				pf.missing = missing.Missing
			}
			return nil, pf
		}
		out.drives = append(out.drives, res.Drives...)
		out.charges = append(out.charges, res.Charges...)
		// MaxSessions bounds one file; the request as a whole is held to the same number of each kind.
		if len(out.drives) > histimport.MaxSessions || len(out.charges) > histimport.MaxSessions {
			return nil, failureOf(histimport.ErrTooBig)
		}
		mergeReport(&out.report, res.Report)
	}
	return out, nil
}

func openTable(f importFile) (histimport.Table, error) {
	if f.ext() == "xlsx" {
		return histimport.OpenXLSX(bytes.NewReader(f.data), int64(len(f.data)))
	}
	return histimport.OpenCSV(bytes.NewReader(f.data))
}

func mergeReport(dst *histimport.Report, r histimport.Report) {
	dst.Rows += r.Rows
	dst.Accepted += r.Accepted
	dst.BadRows += r.BadRows
	dst.Ambiguous += r.Ambiguous
	dst.DroppedCoords += r.DroppedCoords
	for k, v := range r.Skipped {
		dst.Skipped[k] += v
	}
	if dst.SoCScale == "" {
		dst.SoCScale = r.SoCScale
	}
	if dst.DistanceUnit == "" {
		dst.DistanceUnit = r.DistanceUnit
	}
	if dst.EnergyUnit == "" {
		dst.EnergyUnit = r.EnergyUnit
	}
	dst.Vehicles = append(dst.Vehicles, r.Vehicles...)
	if len(dst.Vehicles) > 20 {
		dst.Vehicles = dst.Vehicles[:20]
	}
	if !r.First.IsZero() && (dst.First.IsZero() || r.First.Before(dst.First)) {
		dst.First = r.First
	}
	if r.Last.After(dst.Last) {
		dst.Last = r.Last
	}
}

// parseFailure is a parse refused, ready to send. Header names are the file's own and carry no row data.
type parseFailure struct {
	status   int
	code     string
	msg      string
	header   []string
	cells    int // non-empty cells in the file's first line, before nameLikeCells (the log's test of whether it was a header row)
	missing  []string
	expected []histimport.ExpectedLayout
	choices  []string
	vehicles []string
}

// unknownColumnsMessage tells the user what a file must contain: for each layout of the source, the required columns with example
// headers. It never carries anything from the file.
func unknownColumnsMessage(layouts []histimport.ExpectedLayout) string {
	var b strings.Builder
	b.WriteString("This file is not an export we can read: its columns do not match. We need")
	for i, l := range layouts {
		if i > 0 {
			b.WriteString(";")
		}
		b.WriteString(" for " + l.Kind + ": ")
		for j, c := range l.Columns {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(c.Field)
			if len(c.Examples) > 0 {
				b.WriteString(` (e.g. "` + strings.Join(c.Examples, `", "`) + `")`)
			}
		}
	}
	b.WriteString(". Nothing was imported.")
	return b.String()
}

// refusedReason maps a refusal code to the label of rivolt_history_import_rejected_total; "" for one that is not a rejection of the file
// (the preview asking the user for a choice).
func refusedReason(code string) string {
	switch code {
	case "unknown_columns":
		return "unknown_header"
	case "too_many_bad_rows":
		return "bad_row"
	case "too_big":
		return "size"
	case "need_choice":
		return ""
	}
	return "parse"
}

// refused counts a file refused at parse and logs what the operator needs to tell the first failing real export from noise: the source, the
// reason and the header names the file had (nameLikeCells keeps names only, never a cell that looks like data), no values.
func (a *historyImportAPI) refused(source string, pf *parseFailure) {
	reason := refusedReason(pf.code)
	if reason == "" {
		return
	}
	if a.d.Metrics != nil {
		a.d.Metrics.HistoryImportRejected.WithLabelValues(source, reason).Inc()
	}
	a.logr.Warn("history import refused", "source", source, "reason", reason, "header", logHeader(pf), "missing", pf.missing)
}

func failureOf(err error) *parseFailure {
	var nc *histimport.NeedChoiceError
	var bad *histimport.TooManyBadRowsError
	switch {
	case errors.As(err, &nc):
		return &parseFailure{status: http.StatusUnprocessableEntity, code: "need_choice", msg: "The file does not say everything the numbers depend on.", choices: nc.Choices, vehicles: nc.Vehicles}
	case errors.As(err, &bad):
		return &parseFailure{status: http.StatusUnprocessableEntity, code: "too_many_bad_rows", msg: "Too many rows in this file failed the checks. It is probably not the export we expect."}
	case errors.Is(err, histimport.ErrTooBig):
		return &parseFailure{status: http.StatusRequestEntityTooLarge, code: "too_big", msg: "That file is larger than we can read. Export a shorter range."}
	}
	return &parseFailure{status: http.StatusUnprocessableEntity, code: "bad_file", msg: "That file could not be read as a CSV or Excel export."}
}

// logHeader is what a refusal may put in the operator log. A file with no header row has a data row in its first line (names, emails,
// addresses), and nameLikeCells lets words through, so the log is stricter than the response: cells with an "@" or a digit are dropped, and
// the rest are logged only when most of the line was header-like; otherwise only the count.
func logHeader(pf *parseFailure) any {
	keep := []string{}
	for _, c := range pf.header {
		if !strings.ContainsAny(c, "@0123456789") && len(c) <= 30 {
			keep = append(keep, c)
		}
	}
	if pf.cells == 0 || len(keep)*2 <= pf.cells || !looksLikeHeader(keep) {
		return fmt.Sprintf("%d cells, not logged", pf.cells)
	}
	return keep
}

// exportWords are whole tokens a real export header uses. Bare "mi" and "lon" are left out (they are given names); a real header has
// another word beside them (distance, lat, time).
var exportWords = map[string]bool{
	"time": true, "timestamp": true, "date": true, "datetime": true, "start": true, "started": true, "end": true, "ended": true,
	"battery": true, "soc": true, "distance": true, "km": true, "mph": true, "energy": true, "kwh": true, "speed": true, "odometer": true,
	"lat": true, "latitude": true, "longitude": true, "cost": true, "power": true, "charge": true, "charges": true, "charging": true,
	"charged": true, "drive": true, "drives": true, "driving": true,
}

// looksLikeHeader is whether at least two cells each hold a whole export word. One is not enough: a surname or a place can be a whole
// word too ("Power", "Times Square"), but a data row does not have two such cells.
func looksLikeHeader(cells []string) bool {
	n := 0
	for _, c := range cells {
		for _, tok := range strings.FieldsFunc(strings.ToLower(c), func(r rune) bool { return !unicode.IsLetter(r) }) {
			if exportWords[tok] {
				n++
				break
			}
		}
	}
	return n >= 2
}

func nonEmpty(cells []string) int {
	n := 0
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			n++
		}
	}
	return n
}

// nameLikeCells keeps what looks like a column name from a file's first line. A file with no header row has a data row there, and the
// cells of a data row (times, coordinates, readings) must not travel back to the client: no cell that parses as a number or a date, or
// that is mostly digits, at most 40 characters each and 60 cells.
func nameLikeCells(cells []string) []string {
	out := []string{}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" || len(c) > 40 || !utf8.ValidString(c) || looksLikeData(c) {
			continue
		}
		if out = append(out, c); len(out) == 60 {
			break
		}
	}
	return out
}

var dataLayouts = []string{time.RFC3339, time.RFC3339Nano, "2006-01-02", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "1/2/2006", "1/2/2006 15:04", "1/2/2006 15:04:05",
	"1/2/2006 3:04:05 PM", "1/2/2006 3:04 PM", "02.01.2006", "02.01.2006 15:04", "2006/01/02", "2006/01/02 15:04:05", "15:04:05", "15:04", "Jan 2, 2006", "2 Jan 2006"}

func looksLikeData(c string) bool {
	if _, err := strconv.ParseFloat(strings.ReplaceAll(c, ",", "."), 64); err == nil {
		return true
	}
	for _, l := range dataLayouts {
		if _, err := time.Parse(l, c); err == nil {
			return true
		}
	}
	digits := 0
	for _, r := range c {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits*2 > len(c)
}

func (p *parseFailure) write(w http.ResponseWriter) {
	body := map[string]any{"error": p.msg, "code": p.code}
	if len(p.header) > 0 {
		body["header"] = p.header
	}
	if len(p.missing) > 0 {
		body["missing"] = p.missing
	}
	if len(p.expected) > 0 {
		body["expected"] = p.expected
	}
	if len(p.choices) > 0 {
		body["need"] = p.choices
	}
	if len(p.vehicles) > 0 {
		body["vehicles"] = p.vehicles
	}
	writeJSON(w, p.status, body)
}

// Skip reasons added by the dedupe against stored rows (the parser has its own).
const (
	skipLive     = "overlaps_live"
	skipExisting = "overlaps_existing"
)

// plan is a parse after the dedupe: what a commit would write.
type plan struct {
	drives  []drives.Drive
	charges []charges.Charge
	extra   []map[string]any // per charge: metadata beyond the row's own
	skipped map[string]int
	report  histimport.Report
}

// plan dedupes in the design's order: a duplicate in the file (the parser already kept the first), then a live row, then any other
// stored row. Nothing is written.
func (a *historyImportAPI) plan(ctx context.Context, req *importRequest, p *parsed) (*plan, error) {
	ds, cs := a.d.Drives.For(req.uid), a.d.Charges.For(req.uid)
	if ds == nil || cs == nil {
		return nil, errors.New("stores not initialized")
	}
	pl := &plan{skipped: map[string]int{}, report: p.report}
	for k, v := range p.report.Skipped {
		pl.skipped[k] = v
	}
	// One query per kind per chunk, not two per session: 100k sessions would otherwise be 200k round trips before the first byte goes back.
	drv := make([]span, len(p.drives))
	for i, d := range p.drives {
		drv[i] = span{d.StartedAt, d.EndedAt}
	}
	skip, err := coveredBy(ctx, drv, func(st, en []time.Time) ([]bool, error) { return ds.LiveCoversBatch(ctx, req.rivianID, st, en) },
		func(st, en []time.Time) ([]bool, error) { return ds.CoversBatch(ctx, req.rivianID, st, en) })
	if err != nil {
		return nil, err
	}
	for i, d := range p.drives {
		if skip[i] != "" {
			pl.skipped[skip[i]]++
			continue
		}
		pl.drives = append(pl.drives, driveOf(req, d))
	}
	chg := make([]span, len(p.charges))
	for i, c := range p.charges {
		chg[i] = span{c.StartedAt, c.EndedAt}
	}
	skip, err = coveredBy(ctx, chg, func(st, en []time.Time) ([]bool, error) { return cs.LiveCoversBatch(ctx, req.rivianID, st, en) },
		func(st, en []time.Time) ([]bool, error) { return cs.CoversBatch(ctx, req.rivianID, st, en) })
	if err != nil {
		return nil, err
	}
	for i, c := range p.charges {
		if skip[i] != "" {
			pl.skipped[skip[i]]++
			continue
		}
		ch, extra := chargeOf(req, c)
		pl.charges = append(pl.charges, ch)
		pl.extra = append(pl.extra, extra)
	}
	return pl, nil
}

type span struct{ start, end time.Time }

// importCoverChunk is how many sessions one dedupe query checks.
const importCoverChunk = 1000

// coveredBy returns, per session, the skip reason ("" when the session is new): a live row first, then any stored row, as the design orders them.
func coveredBy(ctx context.Context, spans []span, live, any func(starts, ends []time.Time) ([]bool, error)) ([]string, error) {
	out := make([]string, len(spans))
	for lo := 0; lo < len(spans); lo += importCoverChunk {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hi := min(lo+importCoverChunk, len(spans))
		st, en := make([]time.Time, 0, hi-lo), make([]time.Time, 0, hi-lo)
		for _, sp := range spans[lo:hi] {
			st, en = append(st, sp.start), append(en, sp.end)
		}
		lv, err := live(st, en)
		if err != nil {
			return nil, err
		}
		st, en = st[:0], en[:0]
		var idx []int
		for i, sp := range spans[lo:hi] {
			if lv[i] {
				out[lo+i] = skipLive
				continue
			}
			st, en, idx = append(st, sp.start), append(en, sp.end), append(idx, lo+i)
		}
		if len(st) == 0 {
			continue
		}
		ex, err := any(st, en)
		if err != nil {
			return nil, err
		}
		for j, i := range idx {
			if ex[j] {
				out[i] = skipExisting
			}
		}
	}
	return out, nil
}

func driveOf(req *importRequest, d histimport.Drive) drives.Drive {
	return drives.Drive{
		ID: histimport.ExternalID(d.Source, req.rivianID, "d", d.StartedAt), VehicleID: req.rivianID,
		StartedAt: d.StartedAt, EndedAt: d.EndedAt, StartSoCPct: d.StartSoCPct, EndSoCPct: d.EndSoCPct,
		StartOdometerMi: d.StartOdometerMi, EndOdometerMi: d.EndOdometerMi, DistanceMi: d.DistanceMi,
		StartLat: d.StartLat, StartLon: d.StartLon, EndLat: d.EndLat, EndLon: d.EndLon,
		MaxSpeedMph: d.MaxSpeedMph, AvgSpeedMph: d.AvgSpeedMph, EnergyUsedKWh: d.EnergyUsedKWh,
		Source: d.Source, RoutePolyline: d.RoutePolyline,
	}
}

// chargeOf maps a parsed charge. Its cost is left out in this slice: an imported charge is unpriced, and the totals say so (design §3,
// cost follows in a later slice with its own enum value).
func chargeOf(req *importRequest, c histimport.Charge) (charges.Charge, map[string]any) {
	extra := map[string]any{"energy_basis": c.EnergyBasis}
	if c.EnergyEstimated {
		extra["energy_estimated"] = true
	}
	return charges.Charge{
		ID: histimport.ExternalID(c.Source, req.rivianID, "c", c.StartedAt), VehicleID: req.rivianID,
		StartedAt: c.StartedAt, EndedAt: c.EndedAt, StartSoCPct: c.StartSoCPct, EndSoCPct: c.EndSoCPct,
		EnergyAddedKWh: c.EnergyAddedKWh, MaxPowerKW: c.MaxPowerKW, AvgPowerKW: c.AvgPowerKW,
		FinalState: "charging_complete", Lat: c.Lat, Lon: c.Lon, Source: c.Source,
	}, extra
}

type previewRow struct {
	Kind        string    `json:"kind"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	DistanceMi  float64   `json:"distance_mi,omitempty"`
	EnergyKWh   float64   `json:"energy_kwh,omitempty"`
	StartSoCPct float64   `json:"start_soc_pct"`
	EndSoCPct   float64   `json:"end_soc_pct"`
}

func (pl *plan) sample() []previewRow {
	const keep = 5
	var top []previewRow // the earliest rows, sorted; never more than keep+1 long, whatever the plan holds
	add := func(r previewRow) {
		i := sort.Search(len(top), func(i int) bool { return top[i].StartedAt.After(r.StartedAt) })
		if i >= keep {
			return
		}
		top = append(top, previewRow{})
		copy(top[i+1:], top[i:])
		top[i] = r
		if len(top) > keep {
			top = top[:keep]
		}
	}
	for _, d := range pl.drives {
		add(previewRow{Kind: "drive", StartedAt: d.StartedAt, EndedAt: d.EndedAt, DistanceMi: round2(d.DistanceMi), StartSoCPct: d.StartSoCPct, EndSoCPct: d.EndSoCPct})
	}
	for _, c := range pl.charges {
		add(previewRow{Kind: "charge", StartedAt: c.StartedAt, EndedAt: c.EndedAt, EnergyKWh: round2(c.EnergyAddedKWh), StartSoCPct: c.StartSoCPct, EndSoCPct: c.EndSoCPct})
	}
	return top
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// preview parses and dedupes; it writes nothing.
func (a *historyImportAPI) preview(w http.ResponseWriter, r *http.Request) {
	end, ok := a.begin(w, r)
	if !ok {
		return
	}
	defer end()
	req, ok := a.read(w, r)
	if !ok {
		return
	}
	ctx := histfloor.Bypass(r.Context())
	if a.d.Metrics != nil {
		a.d.Metrics.HistoryImportAttempts.WithLabelValues(req.source).Inc()
	}
	p, pf := req.parse(a.now())
	if pf != nil {
		a.refused(req.source, pf)
		pf.write(w)
		return
	}
	is, err := imports.New(a.d.DB, req.uid)
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	resp := map[string]any{"sha256": req.sha, "source": req.source}
	var dup *imports.AlreadyImported
	if err := is.Check(ctx, histimportSource(req), req.sha); errors.As(err, &dup) {
		resp["already_imported"] = map[string]any{"id": dup.ID, "at": dup.At}
	} else if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	pl, err := a.plan(ctx, req, p)
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	warnings := []string{}
	if !p.report.First.IsZero() {
		if below, err := histfloor.Below(ctx, a.d.DB, req.uid, p.report.First); err == nil && below {
			warnings = append(warnings, "brings_back_deleted_history")
		}
	}
	if p.report.Ambiguous > 0 {
		warnings = append(warnings, "ambiguous_local_times")
	}
	if p.report.DroppedCoords > 0 {
		warnings = append(warnings, "positions_left_out")
	}
	resp["first"], resp["last"] = p.report.First, p.report.Last
	resp["rows"] = p.report.Rows
	resp["new_drives"], resp["new_charges"] = len(pl.drives), len(pl.charges)
	resp["skipped"] = pl.skipped
	resp["assumptions"] = map[string]any{"soc_scale": p.report.SoCScale, "distance_unit": p.report.DistanceUnit, "energy_unit": p.report.EnergyUnit, "tz": req.tz}
	resp["vehicles"] = p.report.Vehicles
	resp["warnings"] = warnings
	resp["sample"] = pl.sample()
	a.logr.Info("history import preview", "source", req.source, "rows", p.report.Rows, "new_drives", len(pl.drives), "new_charges", len(pl.charges))
	writeJSON(w, http.StatusOK, resp)
}

func histimportSource(req *importRequest) string { return req.formats[0].Source }

// commit writes what the preview showed, as NDJSON lines so a proxy sees traffic while it runs.
func (a *historyImportAPI) commit(w http.ResponseWriter, r *http.Request) {
	end, ok := a.begin(w, r)
	if !ok {
		return
	}
	defer end()
	req, ok := a.read(w, r)
	if !ok {
		return
	}
	if req.wantSHA == "" || req.wantSHA != req.sha {
		importError(w, http.StatusConflict, "preview_stale", "These files are not the ones you previewed. Preview again.")
		return
	}
	ctx := histfloor.Bypass(r.Context())
	rc := http.NewResponseController(w)
	push := func() { _ = rc.SetWriteDeadline(a.now().Add(importWriteWindow)) } // the server's WriteTimeout would cut a long commit's stream
	push()
	is, err := imports.New(a.d.DB, req.uid)
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	if n, err := is.Started(ctx, a.now().Add(-time.Hour)); err != nil {
		serverError(w, r, "history import", err)
		return
	} else if n >= importCommitsPerHour {
		w.Header().Set("Retry-After", "600")
		importError(w, http.StatusTooManyRequests, "rate_limited", "You have imported a lot in the last hour. Try again later.")
		return
	}
	p, pf := req.parse(a.now())
	if pf != nil {
		a.refused(req.source, pf)
		pf.write(w)
		return
	}
	// One commit at a time per user, across pods. A second one waiting would send nothing for minutes and hit Cloudflare's 100 s, so it is
	// refused the way a second on this pod is; the client retries once the first has finished and then dedupes against its rows.
	lock, err := a.d.DB.BeginTx(ctx, nil)
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	defer lock.Rollback() //nolint:errcheck // the lock is released either way
	var got bool
	if err := lock.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('history_import'), hashtext($1))`, req.uid.String()).Scan(&got); err != nil {
		serverError(w, r, "history import", err)
		return
	}
	if !got {
		w.Header().Set("Retry-After", "10")
		importError(w, http.StatusTooManyRequests, "in_progress", "Another import of yours is still running. Wait for it to finish.")
		return
	}
	pl, err := a.plan(ctx, req, p)
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	push()
	var vehicleRow uuid.UUID
	if err := a.d.DB.QueryRowContext(ctx, `SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2`, req.uid, req.rivianID).Scan(&vehicleRow); err != nil && !errors.Is(err, sql.ErrNoRows) {
		serverError(w, r, "history import", err)
		return
	}
	batch := imports.Batch{Source: histimportSource(req), Ext: req.files[0].ext(), SHA256: req.sha, Bytes: totalBytes(req.files), Metadata: map[string]any{
		"tz": req.tz, "distance_unit": p.report.DistanceUnit, "energy_unit": p.report.EnergyUnit, "files": len(req.files),
		"first": p.report.First.UTC().Format(time.RFC3339), "last": p.report.Last.UTC().Format(time.RFC3339),
	}}
	if vehicleRow != uuid.Nil {
		batch.VehicleID = &vehicleRow
	}
	id, err := is.Begin(ctx, batch)
	var dup *imports.AlreadyImported
	if errors.As(err, &dup) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "These files were already imported.", "code": "already_imported", "id": dup.ID, "at": dup.At})
		return
	}
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	total := len(pl.drives) + len(pl.charges)
	_ = enc.Encode(map[string]any{"type": "start", "id": id, "total": total})
	flush()
	fail := func(err error) {
		a.logr.Error("history import commit", "err", err, "source", req.source)
		_ = enc.Encode(map[string]any{"type": "error", "id": id, "error": "The import stopped part way. Undo it and try again."})
		flush()
	}
	ds, cs := a.d.Drives.For(req.uid), a.d.Charges.For(req.uid)
	done, nd, nc := 0, 0, 0
	tick := func() {
		done++
		if done%importProgressEvery == 0 {
			push()
			_ = enc.Encode(map[string]any{"type": "progress", "done": done, "total": total})
			flush()
		}
	}
	for _, d := range pl.drives {
		added, err := ds.InsertImported(ctx, id, d, nil)
		if err != nil {
			fail(err)
			return
		}
		if added {
			nd++
		} else {
			pl.skipped[skipExisting]++
		}
		tick()
	}
	for i, c := range pl.charges {
		added, err := cs.InsertImported(ctx, id, c, pl.extra[i])
		if err != nil {
			fail(err)
			return
		}
		if added {
			nc++
		} else {
			pl.skipped[skipExisting]++
		}
		tick()
	}
	skipped := 0
	for _, n := range pl.skipped {
		skipped += n
	}
	if err := is.Finish(ctx, id, imports.Counts{Rows: p.report.Rows, Drives: nd, Charges: nc, Skipped: skipped}); err != nil {
		fail(err)
		return
	}
	if a.d.Metrics != nil {
		a.d.Metrics.HistoryImportCommits.WithLabelValues(req.source).Inc()
		a.d.Metrics.HistoryImportRows.WithLabelValues(req.source, "drives").Add(float64(nd))
		a.d.Metrics.HistoryImportRows.WithLabelValues(req.source, "charges").Add(float64(nc))
	}
	a.logr.Info("history import commit", "source", req.source, "rows", p.report.Rows, "drives", nd, "charges", nc, "skipped", skipped)
	_ = enc.Encode(map[string]any{"type": "done", "id": id, "drives": nd, "charges": nc, "skipped": pl.skipped})
	flush()
}

func totalBytes(fs []importFile) int64 {
	var n int64
	for _, f := range fs {
		n += int64(len(f.data))
	}
	return n
}

// GET /api/imports: the account's batches, newest first.
func (a *historyImportAPI) list(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth.UserFromContext(r.Context())
	if !ok || uid == uuid.Nil {
		importError(w, http.StatusUnauthorized, "unauthenticated", "Sign in first.")
		return
	}
	is, err := imports.New(a.d.DB, uid)
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	items, err := is.List(r.Context())
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": items})
}

// POST /api/imports/{id}/undo: removes everything the batch brought in. 404 when it is not the account's or is already undone.
func (a *historyImportAPI) undo(w http.ResponseWriter, r *http.Request) {
	if !sameSite(w, r) {
		return
	}
	uid, ok := auth.UserFromContext(r.Context())
	if !ok || uid == uuid.Nil {
		importError(w, http.StatusUnauthorized, "unauthenticated", "Sign in first.")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		importError(w, http.StatusNotFound, "not_found", "No such import.")
		return
	}
	is, err := imports.New(a.d.DB, uid)
	if err != nil {
		serverError(w, r, "history import", err)
		return
	}
	u, err := is.Undo(r.Context(), id)
	if errors.Is(err, imports.ErrNotFound) {
		importError(w, http.StatusNotFound, "not_found", "No such import.")
		return
	}
	if err != nil {
		serverError(w, r, "history import", fmt.Errorf("undo: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"drives": u.Drives, "charges": u.Charges})
}
