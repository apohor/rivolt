package samples

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// One bind value per column, and a full bulk chunk within Postgres's
// bind-parameter limit, however many columns vehicle_state gains.
func TestBulkInsertFitsTheBindLimit(t *testing.T) {
	cols := strings.Split(sampleColumns, ",")
	args := (&Store{}).sampleArgs(uuid.Nil, Sample{})
	if len(args) != len(cols) {
		t.Fatalf("sampleArgs gives %d values for %d columns", len(args), len(cols))
	}
	if n := bulkInsertRows * len(cols); n > 65535 {
		t.Errorf("a %d-row chunk binds %d params, over 65,535", bulkInsertRows, n)
	}
}
