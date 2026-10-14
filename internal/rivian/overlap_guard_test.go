package rivian

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSessionOverlapRejected(t *testing.T) {
	excl := &pgconn.PgError{Code: "23P01", ConstraintName: "charges_live_no_overlap"}
	if !sessionOverlapRejected(excl) || !sessionOverlapRejected(fmt.Errorf("upsert: %w", excl)) {
		t.Error("exclusion violation (bare or wrapped) must be recognised")
	}
	for _, err := range []error{
		nil,
		errors.New("23P01"),
		&pgconn.PgError{Code: "23505"}, // unique violation
		&pgconn.PgError{Code: "40001"}, // serialization failure
	} {
		if sessionOverlapRejected(err) {
			t.Errorf("%v must not count as an overlap rejection", err)
		}
	}
}
