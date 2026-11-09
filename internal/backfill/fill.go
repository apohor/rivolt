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
	Columns []FillColumn
}

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
	if len(f.Columns) == 0 {
		return fmt.Errorf("fill: no columns declared")
	}
	for _, c := range f.Columns {
		if !identRE.MatchString(c.Name) || c.Name == f.Key {
			return fmt.Errorf("fill: column %q must be a plain identifier and not the key", c.Name)
		}
		if strings.TrimSpace(c.Expr) == "" || strings.Contains(c.Expr, ";") || strings.Contains(c.Empty, ";") {
			return fmt.Errorf("fill: column %s needs a single SQL expression (no semicolons)", c.Name)
		}
	}
	return nil
}

func (c FillColumn) empty() string {
	if strings.TrimSpace(c.Empty) != "" {
		return "(" + c.Empty + ")"
	}
	return "t." + c.Name + " IS NULL"
}

// sql is the batch statement: $1 is the cursor (the last key done, "" at the start), $2 the limit. It returns the number of rows changed and the
// greatest key among them.
func (f Fill) sql() string {
	var anyEmpty, sets []string
	for _, c := range f.Columns {
		e := c.empty()
		anyEmpty = append(anyEmpty, e)
		sets = append(sets, fmt.Sprintf("%s = CASE WHEN %s THEN %s ELSE t.%s END", c.Name, e, c.Expr, c.Name))
	}
	where := strings.Join(anyEmpty, " OR ")
	return fmt.Sprintf(`
		WITH b AS (
		    SELECT t.%[2]s FROM %[1]s t
		     WHERE (($1)::text = '' OR t.%[2]s > ($1)::%[3]s) AND (%[4]s)
		     ORDER BY t.%[2]s LIMIT $2
		       FOR UPDATE OF t),
		u AS (
		    UPDATE %[1]s t SET %[5]s
		      FROM b WHERE t.%[2]s = b.%[2]s AND (%[4]s)
		 RETURNING t.%[2]s)
		SELECT count(*), max(%[2]s)::text FROM u`,
		f.Table, f.Key, f.KeyType, where, strings.Join(sets, ", "))
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
