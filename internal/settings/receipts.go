package settings

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// keyRivianReceipts is the user's opt-in to reading their Rivian charging
// receipts (what Rivian billed). Absent or anything but "1" means off.
const keyRivianReceipts = "privacy.rivian_receipts"

// RivianReceiptsOptIn reports whether the user opted in. Off by default.
func RivianReceiptsOptIn(ctx context.Context, s *Store) bool {
	if s == nil {
		return false
	}
	all, err := s.GetAll(ctx)
	return err == nil && all[keyRivianReceipts] == "1"
}

// SetRivianReceiptsOptIn records the choice.
func SetRivianReceiptsOptIn(ctx context.Context, s *Store, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.Set(ctx, keyRivianReceipts, v)
}

// RivianReceiptsOptInLocked reports the user's receipts opt-in inside tx
// and share-locks their setting row until tx ends. A receipts write done in
// the same tx can then never land after an opt-out: an opt-out that commits
// first is seen here (no row qualifies), and one that comes later waits for
// tx, so the Purge that follows it deletes what tx wrote.
func RivianReceiptsOptInLocked(ctx context.Context, tx *sql.Tx, userID uuid.UUID) (bool, error) {
	var on bool
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(value->>$3 = '1', false) FROM user_settings
		 WHERE user_id = $1 AND namespace = $2
		 FOR SHARE`, userID, namespaceFor(keyRivianReceipts), keyRivianReceipts).Scan(&on)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return on, err
}

// RivianReceiptsOptInSQL is a predicate on a user id column (e.g.
// "r.user_id") that holds when the user opted in, for set-based sweeps.
func RivianReceiptsOptInSQL(userIDCol string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM user_settings us WHERE us.user_id = %s AND us.namespace = '%s' AND us.value->>'%s' = '1')`,
		userIDCol, namespaceFor(keyRivianReceipts), keyRivianReceipts)
}
