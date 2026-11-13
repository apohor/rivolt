//go:build integration

package db_test

// The scale and lock probe (migrations v2, step 4; design: migrations-v2-design.md). The lints read a file's text; this runs it. The base
// schema (every embedded file before the first new one) gets a hand-written seed (testdata/scale_seed.sql: 10^5 rows in each growable table,
// vehicle_state over three partitions), then the new files are applied one by one while a writer inserts into vehicle_state, drives and
// charges every 50 ms. Per file it reports the locks held at commit, whether a growable table was rewritten (relfilenode changed), the
// sequential scans of growable tables, the duration and the writer's longest stall. It fails on:
//   - a rewrite of a growable table, unless the file carries "-- probe: allow-rewrite";
//   - a sequential scan of a growable table in a transaction that held a write-blocking lock on it;
//   - a writer stall over 1 s;
//   - a transactional file over 2 s;
//   - a writer insert, or the seed insert re-run on the new schema, that the new schema rejects (the old binary's write shape).
// A second pass (M4) holds a 10 s ACCESS SHARE reader on each growable table a file names, so an ACCESS EXCLUSIVE request queues: the file
// must still apply within the 30 s retry budget. That pass reports stalls and does not fail on them (a queued lock stalls writers up to
// lock_timeout by design); it fails if the file does not apply.
//
// MIGRATION_PROBE_NEW is a comma list of embedded migration ids (the PR's added files); empty skips. Scale is a fixed design figure; no
// production counts live here, and DA multiplies a per-10^5 timing by the private counts when it matters.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

const (
	probeMaxStall     = time.Second
	probeMaxTxFile    = 2 * time.Second
	probeReaderHold   = 10 * time.Second
	probeRetryBudget  = 30 * time.Second
	probeWriterPeriod = 50 * time.Millisecond
	probeLimitsFrom   = 120 // the runner's lock_timeout and statement_timeout apply from this file number on (migrate.go)
)

var (
	probeGrowable = []string{"vehicle_state", "drives", "charges"}
	// Lock modes that block INSERT and UPDATE on the table.
	probeBlocksWrites = map[string]bool{"ShareLock": true, "ShareRowExclusiveLock": true, "ExclusiveLock": true, "AccessExclusiveLock": true}
	probeLockRank     = map[string]int{"AccessShareLock": 1, "RowShareLock": 2, "RowExclusiveLock": 3, "ShareUpdateExclusiveLock": 4, "ShareLock": 5, "ShareRowExclusiveLock": 6, "ExclusiveLock": 7, "AccessExclusiveLock": 8}
	probeNumber       = regexp.MustCompile(`^(\d+)`)
)

type probeResult struct {
	id       string
	noTx     bool
	dur      time.Duration
	locks    map[string]string // growable table -> strongest lock held at commit
	rewrote  []string
	seqScans map[string]int64
	stall    time.Duration
	attempts int
	failures []string
}

func (r *probeResult) fail(format string, a ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, a...))
}

// probeWriter is the old binary's write load: one row into each growable table every 50 ms.
type probeWriter struct {
	d      *sql.DB
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	max    time.Duration
	cycles int
	errs   []string
}

func startProbeWriter(ctx context.Context, d *sql.DB) *probeWriter {
	w := &probeWriter{d: d, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		tick := time.NewTicker(probeWriterPeriod)
		defer tick.Stop()
		const (
			user    = "00000000-0000-4000-8000-00000000a001"
			vehicle = "00000000-0000-4000-8000-00000000b001"
		)
		for n := int64(1); ; n++ {
			select {
			case <-w.stop:
				return
			case <-tick.C:
			}
			stmts := []string{
				`INSERT INTO vehicle_state (vehicle_id, at, user_id, battery_level_pct, source) VALUES ('` + vehicle + `', now() + $1::bigint * INTERVAL '1 microsecond', '` + user + `', 50, 'probe-writer')`,
				`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, source) VALUES ('` + user + `', '` + vehicle + `', 'w-' || $1::bigint::text, now(), now(), 'probe-writer')`,
				`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source) VALUES ('` + user + `', '` + vehicle + `', 'w-' || $1::bigint::text, now(), now(), 'probe-writer')`,
			}
			for _, q := range stmts {
				c, cancel := context.WithTimeout(ctx, 40*time.Second)
				start := time.Now()
				_, err := d.ExecContext(c, q, n)
				lat := time.Since(start)
				cancel()
				w.mu.Lock()
				w.max = max(w.max, lat)
				if err != nil {
					w.errs = append(w.errs, err.Error())
				}
				w.mu.Unlock()
			}
			w.mu.Lock()
			w.cycles++
			w.mu.Unlock()
		}
	}()
	return w
}

// window runs f and returns the longest insert latency and the insert errors seen while it ran, plus two full cycles after (an insert
// blocked behind f's locks finishes after f does).
func (w *probeWriter) window(f func()) (time.Duration, []string) {
	w.cycleAfter(1) // an insert still in flight from the last window must not count in this one
	w.mu.Lock()
	w.max, w.errs = 0, nil
	w.mu.Unlock()
	f()
	w.cycleAfter(2)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.max, append([]string(nil), w.errs...)
}

// cycleAfter waits for n more full writer cycles (45 s at most).
func (w *probeWriter) cycleAfter(n int) {
	w.mu.Lock()
	target := w.cycles + n
	w.mu.Unlock()
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		w.mu.Lock()
		ok := w.cycles >= target
		w.mu.Unlock()
		if ok {
			return
		}
	}
}

func (w *probeWriter) close() { close(w.stop); <-w.done }

func probeIsNoTx(body string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	return strings.HasPrefix(strings.TrimSpace(first), "-- migrate:no-transaction")
}

func probeLimits(id string) bool {
	m := probeNumber.FindString(id)
	n, err := strconv.Atoi(m)
	return err == nil && n >= probeLimitsFrom
}

// probeBuild returns a database at the base schema plus the seed (tag 1).
func probeBuild(ctx context.Context, t *testing.T, base []db.MigrationFile, seed string) *sql.DB {
	t.Helper()
	d, err := sql.Open("pgx", testpg.Fresh(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(16)
	t.Cleanup(func() { d.Close() })
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS migrations (id TEXT PRIMARY KEY, applied_at BIGINT NOT NULL, note TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range base {
		if err := db.RunMigrationForTest(ctx, d, m.ID, m.SQL); err != nil {
			t.Fatalf("base migration %s: %v", m.ID, err)
		}
	}
	started := time.Now()
	if _, err := d.ExecContext(ctx, strings.ReplaceAll(seed, "__TAG__", "1")); err != nil {
		t.Fatalf("seed on the base schema: %v", err)
	}
	t.Logf("base schema at %s; seed loaded in %s", base[len(base)-1].ID, time.Since(started).Round(time.Millisecond))
	if _, err := d.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition(now())`); err != nil {
		t.Fatal(err)
	}
	return d
}

// probeApplyTx applies one transactional file the way run does (body, then the migrations row) and inspects the transaction before commit.
func probeApplyTx(ctx context.Context, d *sql.DB, f db.MigrationFile, res *probeResult) error {
	started := time.Now()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if probeLimits(f.ID) {
		if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'; SET LOCAL statement_timeout = '60s'; SET LOCAL idle_in_transaction_session_timeout = '30s'`); err != nil {
			return err
		}
	}
	before, err := probeFilenodes(ctx, d)
	if err != nil {
		return err
	}
	// pg_stat_xact_user_tables also carries this backend's not yet flushed counts from the previous transaction on the pooled connection,
	// so take the baseline inside this transaction and report the difference.
	scans0, err := probeXactScans(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, strings.TrimSpace(f.SQL)); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO migrations (id, applied_at, note) VALUES ($1, $2, '')`, f.ID, time.Now().Unix()); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	// Inspect before commit: the locks are still held, and this transaction's own scans and rewrites are visible.
	locks, err := probeLocks(ctx, tx)
	if err != nil {
		return err
	}
	scans, err := probeXactScans(ctx, tx)
	if err != nil {
		return err
	}
	for tbl, n := range scans0 {
		if scans[tbl] -= n; scans[tbl] <= 0 {
			delete(scans, tbl)
		}
	}
	after, err := probeFilenodesTx(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	res.dur = time.Since(started)
	res.locks = map[string]string{}
	for tbl, modes := range locks {
		best := ""
		for m := range modes {
			if probeLockRank[m] > probeLockRank[best] {
				best = m
			}
		}
		res.locks[tbl] = best
	}
	res.seqScans = scans
	for rel, fn := range after {
		if old, ok := before[rel]; ok && old != fn {
			res.rewrote = append(res.rewrote, probeGroup(rel))
		}
	}
	sort.Strings(res.rewrote)
	return nil
}

// probeGroup maps a partition name to its parent so the report names vehicle_state, not vehicle_state_y2026_01.
func probeGroup(rel string) string {
	if strings.HasPrefix(rel, "vehicle_state") {
		return "vehicle_state"
	}
	return rel
}

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

const probeRelFilter = `c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p') AND (c.relname IN ('vehicle_state', 'drives', 'charges') OR c.relname LIKE 'vehicle_state\_y%')`

func probeFilenodes(ctx context.Context, q querier) (map[string]int64, error) {
	return probeFilenodesTx(ctx, q)
}

func probeFilenodesTx(ctx context.Context, q querier) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.relname, c.relfilenode::bigint FROM pg_class c WHERE c.relkind = 'r' AND `+probeRelFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var n string
		var fn int64
		if err := rows.Scan(&n, &fn); err != nil {
			return nil, err
		}
		out[n] = fn
	}
	return out, rows.Err()
}

// probeLocks is the relation locks this backend holds on the growable tables (and their partitions), by table.
func probeLocks(ctx context.Context, q querier) (map[string]map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.relname, l.mode FROM pg_locks l JOIN pg_class c ON c.oid = l.relation
		WHERE l.pid = pg_backend_pid() AND l.locktype = 'relation' AND l.granted AND `+probeRelFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var n, mode string
		if err := rows.Scan(&n, &mode); err != nil {
			return nil, err
		}
		g := probeGroup(n)
		if out[g] == nil {
			out[g] = map[string]bool{}
		}
		out[g][mode] = true
	}
	return out, rows.Err()
}

func probeXactScans(ctx context.Context, q querier) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT relname, seq_scan FROM pg_stat_xact_user_tables WHERE seq_scan > 0 AND (relname IN ('vehicle_state', 'drives', 'charges') OR relname LIKE 'vehicle_state\_y%')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var n string
		var c int64
		if err := rows.Scan(&n, &c); err != nil {
			return nil, err
		}
		out[probeGroup(n)] += c
	}
	return out, rows.Err()
}

// probeHold takes an ACCESS SHARE lock on each table and keeps it for the hold time or until release closes.
func probeHold(ctx context.Context, d *sql.DB, tables []string, release <-chan struct{}) (func(), error) {
	var wg sync.WaitGroup
	for _, tbl := range tables {
		conn, err := d.Conn(ctx)
		if err != nil {
			return nil, err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM `+tbl+` LIMIT 1`); err != nil {
			tx.Rollback()
			conn.Close()
			return nil, err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-time.After(probeReaderHold):
			case <-release:
			}
			_ = tx.Rollback()
			conn.Close()
		}()
	}
	return wg.Wait, nil
}

func probeNamedTables(body string) []string {
	var out []string
	for _, tbl := range probeGrowable {
		if regexp.MustCompile(`(?i)\b` + tbl + `\b`).MatchString(body) {
			out = append(out, tbl)
		}
	}
	return out
}

func probeApply(ctx context.Context, d *sql.DB, f db.MigrationFile, retry bool, res *probeResult) error {
	if probeIsNoTx(f.SQL) {
		res.noTx = true
		started := time.Now()
		res.attempts = 1
		err := db.RunMigrationForTest(ctx, d, f.ID, f.SQL)
		res.dur = time.Since(started)
		return err
	}
	deadline := time.Now().Add(probeRetryBudget)
	for {
		res.attempts++
		err := probeApplyTx(ctx, d, f, res)
		var pe *pgconn.PgError
		if err != nil && retry && errors.As(err, &pe) && (pe.Code == "55P03" || pe.Code == "40P01") && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return err
	}
}

// probeRun applies the new files in order on a fresh seeded base and returns one result per file. With reader set it is the M4 pass.
func probeRun(ctx context.Context, t *testing.T, base, files []db.MigrationFile, seed string, reader bool) []*probeResult {
	t.Helper()
	d := probeBuild(ctx, t, base, seed)
	w := startProbeWriter(ctx, d)
	defer w.close()
	var out []*probeResult
	for _, f := range files {
		res := &probeResult{id: f.ID}
		out = append(out, res)
		release := make(chan struct{})
		var wait func()
		if reader {
			var err error
			if wait, err = probeHold(ctx, d, probeNamedTables(f.SQL), release); err != nil {
				t.Fatal(err)
			}
		}
		var applyErr error
		stall, werrs := w.window(func() { applyErr = probeApply(ctx, d, f, reader, res) })
		close(release)
		if wait != nil {
			wait()
		}
		res.stall = stall
		if applyErr != nil {
			res.fail("did not apply: %v", applyErr)
			continue
		}
		if len(werrs) > 0 {
			res.fail("the old write shape was rejected: %s", werrs[0])
		}
		if reader {
			continue // the lock-queue pass reports stalls and scans caused by its own reader; it asserts only that the file applies
		}
		for _, tbl := range res.rewrote {
			if !strings.Contains(f.SQL, "-- probe: allow-rewrite") {
				res.fail("rewrote %s (relfilenode changed); a deliberate rewrite carries `-- probe: allow-rewrite`", tbl)
			}
		}
		for tbl, n := range res.seqScans {
			if probeBlocksWrites[res.locks[tbl]] {
				res.fail("%d sequential scan(s) of %s under %s, which blocks writes", n, tbl, res.locks[tbl])
			}
		}
		if res.stall > probeMaxStall {
			res.fail("the writer stalled %s (limit %s)", res.stall.Round(time.Millisecond), probeMaxStall)
		}
		if !res.noTx && res.dur > probeMaxTxFile {
			res.fail("the transaction ran %s at this scale (limit %s)", res.dur.Round(time.Millisecond), probeMaxTxFile)
		}
	}
	// The old binary's insert shape against the new schema.
	if len(files) > 0 {
		if _, err := d.ExecContext(ctx, strings.ReplaceAll(seed, "__TAG__", "2")); err != nil {
			last := out[len(out)-1]
			last.fail("the seed insert re-run on the new schema was rejected: %v", err)
		}
	}
	return out
}

func probeReport(t *testing.T, title string, rs []*probeResult) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n| file | tx | duration | locks (growable) | rewrite | seq scans | writer stall | tries | verdict |\n|---|---|---|---|---|---|---|---|---|\n", title)
	for _, r := range rs {
		var locks, scans []string
		for tbl, m := range r.locks {
			locks = append(locks, tbl+": "+strings.TrimSuffix(m, "Lock"))
		}
		for tbl, n := range r.seqScans {
			scans = append(scans, fmt.Sprintf("%s x%d", tbl, n))
		}
		sort.Strings(locks)
		sort.Strings(scans)
		tx, verdict := "yes", "ok"
		if r.noTx {
			tx, locks = "no (concurrent)", []string{"ShareUpdateExclusive (concurrent build)"}
		}
		if len(r.failures) > 0 {
			verdict = "FAIL: " + strings.Join(r.failures, "; ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %d | %s |\n", r.id, tx, r.dur.Round(time.Millisecond), orNone(locks), orNone(r.rewrote), orNone(scans), r.stall.Round(time.Millisecond), r.attempts, verdict)
	}
	t.Log("\n" + b.String())
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
			fmt.Fprintln(f, b.String())
			f.Close()
		}
	}
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func probeSeed(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/scale_seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// probeSplit returns every embedded file that is not one of ids (the base: what main already has) and the files named by ids, in apply order.
func probeSplit(t *testing.T, ids []string) (base, files []db.MigrationFile) {
	t.Helper()
	all, err := db.MigrationFilesForTest()
	if err != nil {
		t.Fatal(err)
	}
	base, files, missing := splitProbeFiles(all, ids)
	if len(missing) > 0 {
		t.Fatalf("not embedded migrations: %v", missing)
	}
	if len(files) == 0 {
		t.Fatal("no files to probe")
	}
	return base, files
}

func splitProbeFiles(all []db.MigrationFile, ids []string) (base, files []db.MigrationFile, missing []string) {
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	for _, m := range all {
		if want[m.ID] {
			files = append(files, m)
			delete(want, m.ID)
		} else {
			base = append(base, m)
		}
	}
	for id := range want {
		missing = append(missing, id)
	}
	sort.Strings(missing)
	return base, files, missing
}

// A PR that adds a file numbered below one already on main is probed against everything else on main, not only the files below it.
func TestProbeSplitBaseIsEveryOtherFile(t *testing.T) {
	all := []db.MigrationFile{{ID: "0001_a"}, {ID: "0002_b"}, {ID: "0003_c"}, {ID: "0004_d"}}
	base, files, missing := splitProbeFiles(all, []string{"0002_b", "0003_c"})
	if len(missing) != 0 || len(files) != 2 || files[0].ID != "0002_b" || files[1].ID != "0003_c" {
		t.Fatalf("files %v missing %v", files, missing)
	}
	if len(base) != 2 || base[0].ID != "0001_a" || base[1].ID != "0004_d" {
		t.Fatalf("base %v: want 0001_a and 0004_d (the later file included)", base)
	}
	if _, _, missing := splitProbeFiles(all, []string{"0009_x"}); len(missing) != 1 {
		t.Fatalf("an unknown id must be reported, got %v", missing)
	}
}

func probeBoth(ctx context.Context, t *testing.T, base, files []db.MigrationFile) (a, b []*probeResult) {
	seed := probeSeed(t)
	a = probeRun(ctx, t, base, files, seed, false)
	probeReport(t, "Scale and lock probe", a)
	b = probeRun(ctx, t, base, files, seed, true)
	probeReport(t, "Lock-queue pass (10 s ACCESS SHARE reader on each growable table the file names)", b)
	return a, b
}

// The PR's added migration files (MIGRATION_PROBE_NEW, set by the migration-probe workflow).
func TestMigrationProbe(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("MIGRATION_PROBE_NEW"))
	if raw == "" {
		t.Skip("MIGRATION_PROBE_NEW is empty: no migration file added")
	}
	base, files := probeSplit(t, strings.Split(raw, ","))
	a, b := probeBoth(context.Background(), t, base, files)
	for _, rs := range [][]*probeResult{a, b} {
		for _, r := range rs {
			for _, f := range r.failures {
				t.Errorf("%s: %s", r.id, f)
			}
		}
	}
}

// The history-import files (0112 to 0118) are the reference for a clean expand: every one must pass, so a probe that flags them is wrong.
func TestMigrationProbePassesTheImportFiles(t *testing.T) {
	all, err := db.MigrationFilesForTest()
	if err != nil {
		t.Fatal(err)
	}
	// The base is the files before 0112 (the tree as it was); the files after 0118, if any, did not exist then.
	var base, files []db.MigrationFile
	for _, m := range all {
		switch {
		case m.ID < "0112":
			base = append(base, m)
		case m.ID < "0119":
			files = append(files, m)
		}
	}
	a, b := probeBoth(context.Background(), t, base, files)
	for _, rs := range [][]*probeResult{a, b} {
		for _, r := range rs {
			for _, f := range r.failures {
				t.Errorf("%s: %s", r.id, f)
			}
		}
	}
}

// Each fixture breaks one rule; the probe must name it. The control passes.
func TestMigrationProbeFailsTheBadFixtures(t *testing.T) {
	all, err := db.MigrationFilesForTest()
	if err != nil {
		t.Fatal(err)
	}
	// The base is everything through 0112 (imports.undone_at exists for the foreign key fixture).
	var base []db.MigrationFile
	for _, m := range all {
		if m.ID < "0113" {
			base = append(base, m)
		}
	}
	fixtures := []struct {
		db.MigrationFile
		want string // "" = must pass
	}{
		{db.MigrationFile{ID: "0900_fixture_control", SQL: "SET LOCAL lock_timeout = '2s';\nALTER TABLE drives ADD COLUMN IF NOT EXISTS probe_ok TEXT;"}, ""},
		{db.MigrationFile{ID: "0901_fixture_fk", SQL: "SET LOCAL lock_timeout = '2s';\nALTER TABLE drives ADD COLUMN IF NOT EXISTS probe_fk UUID;\nALTER TABLE drives ADD CONSTRAINT probe_fk_fkey FOREIGN KEY (probe_fk) REFERENCES imports(id);"}, "sequential scan"},
		{db.MigrationFile{ID: "0902_fixture_volatile_default", SQL: "SET LOCAL lock_timeout = '2s';\nALTER TABLE charges ADD COLUMN probe_v DOUBLE PRECISION NOT NULL DEFAULT random();"}, "rewrote charges"},
		{db.MigrationFile{ID: "0903_fixture_slow_tx", SQL: "SELECT pg_sleep(2.3);"}, "transaction ran"},
		{db.MigrationFile{ID: "0904_fixture_lock_stall", SQL: "SET LOCAL lock_timeout = '2s';\nLOCK TABLE vehicle_state IN ACCESS EXCLUSIVE MODE;\nSELECT pg_sleep(1.4);"}, "writer stalled"},
		{db.MigrationFile{ID: "0906_fixture_constant_default", SQL: "SET LOCAL lock_timeout = '2s';\nALTER TABLE charges ADD COLUMN probe_c INTEGER NOT NULL DEFAULT 0;"}, ""},
		{db.MigrationFile{ID: "0907_fixture_column_type", SQL: "SET LOCAL lock_timeout = '2s';\nALTER TABLE drives ALTER COLUMN max_speed_mph TYPE NUMERIC(8, 2);"}, "rewrote drives"},
		{db.MigrationFile{ID: "0905_fixture_new_check", SQL: "SET LOCAL lock_timeout = '2s';\nALTER TABLE drives ADD CONSTRAINT probe_never CHECK (source = 'never') NOT VALID;"}, "old write shape was rejected"},
	}
	var files []db.MigrationFile
	for _, f := range fixtures {
		files = append(files, f.MigrationFile)
	}
	rs := probeRun(context.Background(), t, base, files, probeSeed(t), false)
	probeReport(t, "Fixtures", rs)
	for i, f := range fixtures {
		got := strings.Join(rs[i].failures, "; ")
		switch {
		case f.want == "" && got != "":
			t.Errorf("%s: the control must pass, got %s", f.ID, got)
		case f.want != "" && !strings.Contains(got, f.want):
			t.Errorf("%s: want a failure containing %q, got %q", f.ID, f.want, got)
		}
	}
}
