package backfill

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// Fill is the ready-made job body for the usual backfill: fill new columns of an existing table from the row's own columns, in key order, only
// where they are still empty. The UPDATE is built here, so what a job may write is fixed by its declaration and enforced in the SQL itself:
//   - only the declared columns are SET, and each one only where its Empty predicate holds (`CASE WHEN <empty> THEN <expr> ELSE col END`, with the
//     same predicate in the WHERE), so a value the recorder (or a user) has written since is never overwritten, not even by a batch that read the row
//     earlier: the row is locked (FOR UPDATE) and the predicate is re-evaluated against its current contents;
//   - the new value is computed inside the statement from the row's own columns (Expr), never read in Go and written back;
//   - rows are taken in key order after the cursor, a batch at a time, and a row another transaction holds is waited for (bounded by the batch's
//     lock_timeout; a timeout fails the batch, which is retried), never skipped: skipping would leave it behind the cursor for good.
//
// A job on a recorder table (RecorderTables) must be a Fill: Validate refuses a raw Step there.
type Fill struct {
	// Table and Key (the primary key column) name what is walked; KeyType is its SQL type for the cursor cast (uuid, bigint, integer, text,
	// timestamptz).
	Table   string
	Key     string
	KeyType string
	// MoreKeys are the further columns of a composite key, after Key, in order (vehicle_state is keyed by vehicle_id, at). The cursor then covers the
	// whole key (row comparison, `(a, b) > (x, y)`), so rows that share a leading value are walked in full. Key plus MoreKeys must be exactly the
	// columns of one unique index, or the job is refused.
	MoreKeys []FillKey
	Columns  []FillColumn
	// WriterRelease is the final tag (vX.Y.Z) of the release whose writers (the recorder, the API) already fill these columns on every new row.
	// A job is registered only in a later release: a row written behind the cursor by an old writer is never revisited, yet the job would report
	// done. The registration PR shows the writer live in that tag.
	WriterRelease string
}

var releaseRE = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// FillKey is one further column of a composite key.
type FillKey struct{ Name, Type string }

// keys lists the key columns in order.
func (f Fill) keys() []FillKey { return append([]FillKey{{f.Key, f.KeyType}}, f.MoreKeys...) }

// FillColumn is one column a Fill writes.
type FillColumn struct {
	Name string
	// Expr is the SQL expression for the new value, over the row's own columns (referred to as t.<col>) and constants.
	Expr string
	// Empty is the predicate that says the column is still unfilled; "" is `t.<Name> IS NULL`.
	Empty string
}

var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
var keyTypeRE = regexp.MustCompile(`^(uuid|bigint|integer|smallint|text|timestamptz)$`)

// RecorderTables are the tables the live recorder keeps writing (and the repair tools touch): a backfill there must be a Fill, so what it may
// write is declared and enforced in the SQL. Keep in step with db.RepairWriteTables and the partitioned vehicle_state.
var RecorderTables = map[string]bool{"drives": true, "charges": true, "vehicles": true, "vehicle_state": true, "charges_capped_backup": true, "charges_deleted_backup": true}

func (f Fill) validate() error {
	if !identRE.MatchString(f.Table) || !identRE.MatchString(f.Key) || !keyTypeRE.MatchString(f.KeyType) {
		return fmt.Errorf("fill: table %q, key %q and key type %q must be plain identifiers and a known key type", f.Table, f.Key, f.KeyType)
	}
	for _, k := range f.MoreKeys {
		if !identRE.MatchString(k.Name) || !keyTypeRE.MatchString(k.Type) {
			return fmt.Errorf("fill: key column %q (%q) must be a plain identifier and a known key type", k.Name, k.Type)
		}
	}
	if !releaseRE.MatchString(f.WriterRelease) {
		return fmt.Errorf("fill: WriterRelease %q must be the final tag (vX.Y.Z) whose writers already fill the columns; register the job in a later release", f.WriterRelease)
	}
	if len(f.Columns) == 0 {
		return fmt.Errorf("fill: no columns declared")
	}
	for _, c := range f.Columns {
		if !identRE.MatchString(c.Name) || f.isKey(c.Name) {
			return fmt.Errorf("fill: column %q must be a plain identifier and not the key", c.Name)
		}
		if strings.TrimSpace(c.Expr) == "" || strings.Contains(c.Expr, ";") || strings.Contains(c.Empty, ";") {
			return fmt.Errorf("fill: column %s needs a single SQL expression (no semicolons)", c.Name)
		}
	}
	return nil
}

func (f Fill) isKey(name string) bool {
	for _, k := range f.keys() {
		if k.Name == name {
			return true
		}
	}
	return false
}

func (c FillColumn) empty() string {
	if strings.TrimSpace(c.Empty) != "" {
		return "(" + c.Empty + ")"
	}
	return "t." + c.Name + " IS NULL"
}

// sql is the batch statement: $1 is the cursor (the last key done, "" at the start; for a composite key a JSON array of the key columns as text),
// $2 the limit. It returns the number of rows changed and the greatest key among them (in the same form).
func (f Fill) sql() string {
	var anyEmpty, sets []string
	for _, c := range f.Columns {
		e := c.empty()
		anyEmpty = append(anyEmpty, e)
		sets = append(sets, fmt.Sprintf("%s = CASE WHEN %s THEN %s ELSE t.%s END", c.Name, e, c.Expr, c.Name))
	}
	where := strings.Join(anyEmpty, " OR ")
	keys := f.keys()
	var cols, cur, join, desc, bcols []string
	for i, k := range keys {
		cols = append(cols, "t."+k.Name)
		bcols = append(bcols, "t."+k.Name)
		join = append(join, fmt.Sprintf("t.%[1]s = b.%[1]s", k.Name))
		desc = append(desc, k.Name+" DESC")
		if len(keys) == 1 {
			cur = append(cur, fmt.Sprintf("($1)::%s", k.Type))
		} else {
			cur = append(cur, fmt.Sprintf("(($1)::jsonb->>%d)::%s", i, k.Type))
		}
	}
	after := fmt.Sprintf("(%s) > (%s)", strings.Join(cols, ", "), strings.Join(cur, ", "))
	if len(keys) == 1 {
		after = cols[0] + " > " + cur[0]
	}
	order := strings.Join(cols, ", ")
	last := "max(" + keys[0].Name + ")::text"
	if len(keys) > 1 {
		var parts []string
		for _, k := range keys {
			parts = append(parts, k.Name+"::text")
		}
		last = fmt.Sprintf("(array_agg(json_build_array(%s) ORDER BY %s))[1]::text", strings.Join(parts, ", "), strings.Join(desc, ", "))
	}
	var ret []string
	for _, k := range keys {
		ret = append(ret, "t."+k.Name)
	}
	return fmt.Sprintf(`
		WITH b AS (
		    SELECT %[2]s FROM %[1]s t
		     WHERE (($1)::text = '' OR %[3]s) AND (%[4]s)
		     ORDER BY %[6]s LIMIT $2
		       FOR UPDATE OF t),
		u AS (
		    UPDATE %[1]s t SET %[5]s
		      FROM b WHERE %[7]s AND (%[4]s)
		 RETURNING %[8]s)
		SELECT count(*), %[9]s FROM u`,
		f.Table, strings.Join(bcols, ", "), after, where, strings.Join(sets, ", "), order, strings.Join(join, " AND "), strings.Join(ret, ", "), last)
}

// keyError says the Fill's key cannot carry the cursor.
type keyError struct{ msg string }

func (e *keyError) Error() string { return e.msg }

// checkKey refuses a Fill whose key columns are not exactly the columns of one valid, non-partial, plain-column unique index (a primary key counts)
// on the table, in the index's own column order, all of them NOT NULL. Uniqueness keeps the cursor from skipping rows at a batch boundary; the
// order lets each batch's ORDER BY and `(a, b) > (x, y)` use the index instead of sorting the table.
func (f Fill) checkKey(ctx context.Context, db *sql.DB) error {
	var names []string
	for _, k := range f.keys() {
		names = append(names, k.Name)
	}
	var ok bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM pg_index i
		     WHERE i.indrelid = to_regclass($1) AND i.indisunique AND i.indisvalid
		       AND i.indpred IS NULL AND i.indexprs IS NULL AND i.indnkeyatts = $3
		       AND (SELECT array_agg(a.attname::text ORDER BY u.ord)
		              FROM unnest(i.indkey::int2[]) WITH ORDINALITY u(attnum, ord)
		              JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = u.attnum
		             WHERE u.ord <= i.indnkeyatts AND a.attnotnull) = $2::text[])`,
		f.Table, "{"+strings.Join(names, ",")+"}", len(names)).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return &keyError{fmt.Sprintf("fill: %s(%s) is not exactly the not-null columns of one unique index, in its column order; the keyset cursor would skip rows or sort the table", f.Table, strings.Join(names, ", "))}
	}
	return nil
}

// Step makes the Job.Step of a Fill.
func (f Fill) Step() func(ctx context.Context, tx *sql.Tx, cursor string, limit int) (Result, error) {
	q := f.sql()
	return func(ctx context.Context, tx *sql.Tx, cursor string, limit int) (Result, error) {
		var n int
		var last sql.NullString
		if err := tx.QueryRowContext(ctx, q, cursor, limit).Scan(&n, &last); err != nil {
			return Result{}, err
		}
		if n == 0 {
			return Result{Next: cursor, Done: true}, nil
		}
		return Result{Next: last.String, Rows: n}, nil
	}
}

// Validate says whether the job may be registered: a name, a Table, a Step (or a Fill, which makes one), and a Fill when the table is one of
// RecorderTables.
func (j *Job) Validate() error {
	if strings.TrimSpace(j.Name) == "" {
		return fmt.Errorf("backfill: a job needs a name")
	}
	if j.Fill != nil {
		if err := j.Fill.validate(); err != nil {
			return fmt.Errorf("backfill %s: %w", j.Name, err)
		}
		j.Table = j.Fill.Table
		if j.Step == nil {
			j.Step = j.Fill.Step()
		}
		return nil
	}
	if j.Step == nil || j.Table == "" {
		return fmt.Errorf("backfill %s: a job needs a Table and a Step (or a Fill)", j.Name)
	}
	if RecorderTables[j.Table] {
		return fmt.Errorf("backfill %s: %s is a recorder table: declare the job as a Fill so what it writes is fixed and enforced in the SQL", j.Name, j.Table)
	}
	return nil
}
