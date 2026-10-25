package push

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// The cross-user statements (APNs token registration) run on the system pool when the factory has one, and on the store's own
// pool otherwise, which is today's behavior.
func TestCrossUserPoolSelection(t *testing.T) {
	req, sys := new(sql.DB), new(sql.DB)
	u := uuid.New()

	st := NewFactoryWithSystem(req, sys).For(u)
	if st == nil || st.db != req || st.crossUser() != sys {
		t.Errorf("with a system pool: db is the request pool and crossUser is the system pool, got %+v", st)
	}
	st = NewFactory(req).For(u)
	if st == nil || st.crossUser() != req {
		t.Errorf("without one, crossUser is the store's own pool")
	}
	st, err := OpenStore(req, u)
	if err != nil || st.crossUser() != req {
		t.Errorf("OpenStore keeps today's behavior: %v", err)
	}
}
