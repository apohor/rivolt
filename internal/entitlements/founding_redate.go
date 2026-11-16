package entitlements

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The founding re-date: the grant was applied with an end date the launch notice no longer states (the launch moved). It moves the end of
// the founding rows that still carry exactly the old end, and grants the accounts that joined between the first apply and the new launch
// instant. Nothing but current_period_end changes on a moved row; a row with any other end is left alone. Dry run first, an undo file
// before the write, the dry run's counts and hashes to apply, and one transaction that rolls back if any count is off.

// RedatePlan is what the re-date would do, or did.
type RedatePlan struct {
	Eligible int         // accounts that exist at launch, the demo excluded
	Move     []uuid.UUID // founding rows ending exactly at From, sorted
	Grant    []uuid.UUID // accounts at launch with no founding row yet, sorted
	Other    int         // founding rows of those accounts with some other end: left alone
	From     time.Time
	To       time.Time
}

// MoveHash and GrantHash are the sha256 of the sorted ids, in the same form as FoundingReport.IDsHash, so the moved set's hash can be
// compared with the grant's own.
func (p RedatePlan) MoveHash() string  { return hashIDs(p.Move) }
func (p RedatePlan) GrantHash() string { return hashIDs(p.Grant) }

func hashIDs(ids []uuid.UUID) string {
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id.String() + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PlanRedate lists the re-date for launch from the old end. Pure read.
func PlanRedate(ctx context.Context, q querier, demo uuid.UUID, launch, from time.Time) (RedatePlan, error) {
	p := RedatePlan{From: from.UTC(), To: FoundingEnd(launch)}
	rows, err := q.QueryContext(ctx, `
		SELECT u.id, e.user_id IS NOT NULL, e.current_period_end
		  FROM users u
		  LEFT JOIN entitlements e ON e.user_id = u.id AND e.source = 'founding'
		 WHERE u.created_at <= $1 AND u.id <> $2`, launch.UTC(), demo)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var has bool
		var end sql.NullTime
		if err := rows.Scan(&id, &has, &end); err != nil {
			return p, err
		}
		p.Eligible++
		switch {
		case !has:
			p.Grant = append(p.Grant, id)
		case end.Valid && end.Time.Equal(p.From):
			p.Move = append(p.Move, id)
		case end.Valid && end.Time.Equal(p.To):
			// already at the new end
		default:
			p.Other++
		}
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	sort.Slice(p.Move, func(i, j int) bool { return p.Move[i].String() < p.Move[j].String() })
	sort.Slice(p.Grant, func(i, j int) bool { return p.Grant[i].String() < p.Grant[j].String() })
	return p, nil
}

// UndoSQL is the script that puts the database back as it was before the apply: the moved rows return to the old end and the granted
// rows are removed. It is only valid straight after the apply (it matches on the new end).
func (p RedatePlan) UndoSQL() string {
	list := func(ids []uuid.UUID) string {
		s := make([]string, len(ids))
		for i, id := range ids {
			s[i] = "'" + id.String() + "'"
		}
		return strings.Join(s, ", ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- undo of the founding re-date %s -> %s: %d moved, %d granted\nBEGIN;\n", p.From.Format(time.RFC3339), p.To.Format(time.RFC3339), len(p.Move), len(p.Grant))
	if len(p.Move) > 0 {
		fmt.Fprintf(&b, "UPDATE entitlements SET current_period_end = '%s' WHERE source = 'founding' AND current_period_end = '%s' AND user_id IN (%s);\n",
			p.From.Format(time.RFC3339), p.To.Format(time.RFC3339), list(p.Move))
	}
	if len(p.Grant) > 0 {
		fmt.Fprintf(&b, "DELETE FROM entitlements WHERE source = 'founding' AND current_period_end = '%s' AND user_id IN (%s);\n", p.To.Format(time.RFC3339), list(p.Grant))
	}
	b.WriteString("COMMIT;\n")
	return b.String()
}

// ErrRedateCount is a re-date check failing; the transaction is rolled back.
var ErrRedateCount = errors.New("entitlements: the founding re-date did not leave every account at the new end")

// ApplyRedate runs the plan in one transaction and checks it: exactly the planned rows moved, exactly the planned rows granted, and
// afterwards every account at launch has a founding row and none still ends at the old end.
func ApplyRedate(ctx context.Context, d *sql.DB, demo uuid.UUID, launch time.Time, p RedatePlan) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if len(p.Move) > 0 {
		res, err := tx.ExecContext(ctx, `
			UPDATE entitlements SET current_period_end = $1
			 WHERE source = 'founding' AND current_period_end = $2 AND user_id = ANY($3::uuid[])`, p.To, p.From, uuidArray(p.Move))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); int(n) != len(p.Move) {
			return fmt.Errorf("%w: moved %d rows, planned %d", ErrRedateCount, n, len(p.Move))
		}
	}
	if len(p.Grant) > 0 {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO entitlements (user_id, source, status, current_period_end)
			SELECT id, 'founding', 'active', $1 FROM users WHERE id = ANY($2::uuid[])
			ON CONFLICT (user_id, source) DO NOTHING`, p.To, uuidArray(p.Grant))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); int(n) != len(p.Grant) {
			return fmt.Errorf("%w: granted %d rows, planned %d", ErrRedateCount, n, len(p.Grant))
		}
	}
	var accounts, withRow, stale int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*), count(e.user_id), count(*) FILTER (WHERE e.current_period_end = $3)
		  FROM users u LEFT JOIN entitlements e ON e.user_id = u.id AND e.source = 'founding'
		 WHERE u.created_at <= $1 AND u.id <> $2`, launch.UTC(), demo, p.From).Scan(&accounts, &withRow, &stale); err != nil {
		return err
	}
	if accounts != withRow || stale != 0 {
		return fmt.Errorf("%w: %d accounts, %d with a founding row, %d still at the old end", ErrRedateCount, accounts, withRow, stale)
	}
	return tx.Commit()
}

// uuidArray is a Postgres array literal for a uuid[] parameter.
func uuidArray(ids []uuid.UUID) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = id.String()
	}
	return "{" + strings.Join(s, ",") + "}"
}

// RedateExpect is what --apply must be given, from the dry run.
type RedateExpect struct {
	MoveCount, GrantCount int
	MoveSHA, GrantSHA     string
}

// redateRefusal is why --apply must not go ahead, or "".
func redateRefusal(p RedatePlan, e RedateExpect) string {
	switch {
	case len(p.Move) == 0 && len(p.Grant) == 0:
		return "nothing to do: no founding row ends at the old end and every account at launch has one. Nothing was written (a repeat apply is a refusal)"
	case e.MoveCount < 0 || e.GrantCount < 0 || e.MoveSHA == "" || e.GrantSHA == "":
		return fmt.Sprintf("--apply needs --expect-count, --expect-sha, --expect-grant-count and --expect-grant-sha from the dry run (now: move %d %s, grant %d %s). Nothing was written",
			len(p.Move), p.MoveHash(), len(p.Grant), p.GrantHash())
	case e.MoveCount != len(p.Move) || e.MoveSHA != p.MoveHash() || e.GrantCount != len(p.Grant) || e.GrantSHA != p.GrantHash():
		return fmt.Sprintf("the expected counts/hashes are not the dry run's (you passed move %d %s, grant %d %s; now move %d %s, grant %d %s). Nothing was written",
			e.MoveCount, e.MoveSHA, e.GrantCount, e.GrantSHA, len(p.Move), p.MoveHash(), len(p.Grant), p.GrantHash())
	}
	return ""
}

// RunRedate is the re-date mode of `rivolt entitlements-founding`. It returns the exit code.
func RunRedate(ctx context.Context, pool *sql.DB, out, errOut io.Writer, demoID uuid.UUID, launch, from time.Time, apply bool, e RedateExpect, undoFile string) int {
	p, err := PlanRedate(ctx, pool, demoID, launch, from)
	if err != nil {
		fmt.Fprintln(errOut, "entitlements-founding:", err)
		return 1
	}
	if p.From.Equal(p.To) {
		fmt.Fprintln(errOut, "entitlements-founding: --redate-from is the new end already; nothing to re-date")
		return 1
	}
	fmt.Fprintf(out, "re-date: founding end %s -> %s (launch %s)\n", p.From.Format(time.RFC3339), p.To.Format(time.RFC3339), launch.UTC().Format(time.RFC3339))
	fmt.Fprintf(out, "accounts at launch (demo excluded): %d; to move: %d; to grant: %d; founding rows with another end, left alone: %d\n", p.Eligible, len(p.Move), len(p.Grant), p.Other)
	fmt.Fprintf(out, "move ids sha256: %s\ngrant ids sha256: %s\n", p.MoveHash(), p.GrantHash())
	if !apply {
		for _, id := range p.Move {
			fmt.Fprintf(out, "  move  %s\n", id.String()[:8])
		}
		for _, id := range p.Grant {
			fmt.Fprintf(out, "  grant %s\n", id.String()[:8])
		}
		fmt.Fprintln(out, "dry run: nothing written (--apply --expect-count N --expect-sha SHA --expect-grant-count N --expect-grant-sha SHA --undo-file PATH)")
		return 0
	}
	if msg := redateRefusal(p, e); msg != "" {
		fmt.Fprintln(errOut, "entitlements-founding:", msg)
		return 1
	}
	if undoFile == "" {
		fmt.Fprintln(errOut, "entitlements-founding: --apply needs --undo-file (a new file, or - for stdout; it is written before anything changes). Nothing was written")
		return 1
	}
	if undoFile == "-" {
		// The undo script goes to stdout, before the transaction, for an operator who captures it (the pod's filesystem is read-only).
		if _, err := io.WriteString(out, "-- undo script begins\n"+p.UndoSQL()+"-- undo script ends\n"); err != nil {
			fmt.Fprintln(errOut, "entitlements-founding: undo script:", err, ". Nothing was written")
			return 1
		}
	} else {
		f, err := os.OpenFile(undoFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fmt.Fprintln(errOut, "entitlements-founding: undo file:", err, "(it must be a new file). Nothing was written")
			return 1
		}
		_, werr := io.WriteString(f, p.UndoSQL())
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			fmt.Fprintln(errOut, "entitlements-founding: undo file:", werr, ". Nothing was written")
			return 1
		}
	}
	if err := ApplyRedate(ctx, pool, demoID, launch, p); err != nil {
		fmt.Fprintln(errOut, "entitlements-founding:", err, "(rolled back; the undo file is not needed)")
		return 1
	}
	fmt.Fprintf(out, "moved %d, granted %d; every account at launch now has a founding row ending %s. Undo: %s\n", len(p.Move), len(p.Grant), p.To.Format(time.RFC3339), undoFile)
	return 0
}
