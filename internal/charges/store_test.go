package charges

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// nullIfZero must produce an invalid NullFloat64 for the zero
// value. The whole point of the helper is to map "missing /
// unknown" to NULL so the column reads back as untouched on
// re-export instead of polluting averages with bogus 0s. A
// regression here would silently reintroduce the same class of
// bug we wrote it to prevent.
func TestNullIfZero(t *testing.T) {
	cases := []struct {
		name      string
		in        float64
		wantValid bool
		want      float64
	}{
		{"zero", 0, false, 0},
		{"positive", 7.5, true, 7.5},
		// Negative inputs are non-zero, so they must round-trip:
		// a negative SoC delta is still data we want to keep.
		{"negative", -1.25, true, -1.25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nullIfZero(tc.in)
			if got.Valid != tc.wantValid {
				t.Errorf("Valid = %v, want %v", got.Valid, tc.wantValid)
			}
			if tc.wantValid && got.Float64 != tc.want {
				t.Errorf("Float64 = %v, want %v", got.Float64, tc.want)
			}
		})
	}
}

func TestNullIfEmpty(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantValid bool
		want      string
	}{
		{"empty", "", false, ""},
		{"value", "USD", true, "USD"},
		// Whitespace-only is a real string — callers who care
		// (currency codes, etc.) trim before calling.
		{"whitespace", " ", true, " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nullIfEmpty(tc.in)
			if got.Valid != tc.wantValid {
				t.Errorf("Valid = %v, want %v", got.Valid, tc.wantValid)
			}
			if tc.wantValid && got.String != tc.want {
				t.Errorf("String = %q, want %q", got.String, tc.want)
			}
		})
	}
}

// floatFromNull lifts a sql NULL into nil — *not* a zero pointer.
// The UI distinguishes "—" from "0 kWh thermal", and mistaking the
// two is exactly the regression that motivated the helper.
func TestFloatFromNull(t *testing.T) {
	if got := floatFromNull(sql.NullFloat64{}); got != nil {
		t.Errorf("invalid NullFloat64 → %v, want nil", *got)
	}
	if got := floatFromNull(sql.NullFloat64{Float64: 0, Valid: true}); got == nil || *got != 0 {
		t.Errorf("valid 0 NullFloat64 → %v, want pointer to 0", got)
	}
	if got := floatFromNull(sql.NullFloat64{Float64: 4.2, Valid: true}); got == nil || *got != 4.2 {
		t.Errorf("valid 4.2 NullFloat64 → %v, want pointer to 4.2", got)
	}
}

// nullableFloatPtr is the inverse mapping. Same NULL-vs-zero
// concern from the other direction.
func TestNullableFloatPtr(t *testing.T) {
	if got := nullableFloatPtr(nil); got.Valid {
		t.Errorf("nil pointer → Valid=true, want NULL")
	}
	zero := 0.0
	if got := nullableFloatPtr(&zero); !got.Valid || got.Float64 != 0 {
		t.Errorf("pointer to 0 → %+v, want Valid=true, Float64=0", got)
	}
	v := 9.9
	if got := nullableFloatPtr(&v); !got.Valid || got.Float64 != 9.9 {
		t.Errorf("pointer to 9.9 → %+v, want Valid=true, Float64=9.9", got)
	}
}

// OpenStore is the only handler-time guard against the three
// "wiring still nil" cases that would otherwise panic deep in a
// query path. Validate each one explicitly so a future refactor
// can't silently drop a check.
func TestOpenStoreValidation(t *testing.T) {
	d := &sql.DB{} // the helpers don't dereference; only nil/non-nil matters
	uid := uuid.New()
	resolver := &db.VehicleResolver{}

	if _, err := OpenStore(nil, uid, resolver); err == nil {
		t.Error("OpenStore(nil db) returned nil error")
	}
	if _, err := OpenStore(d, uuid.Nil, resolver); err == nil {
		t.Error("OpenStore(zero userID) returned nil error")
	}
	if _, err := OpenStore(d, uid, nil); err == nil {
		t.Error("OpenStore(nil resolver) returned nil error")
	}
	s, err := OpenStore(d, uid, resolver)
	if err != nil {
		t.Fatalf("OpenStore(valid args): err = %v", err)
	}
	if s == nil {
		t.Fatal("OpenStore(valid args): store is nil")
	}
	if s.userID != uid {
		t.Errorf("store userID = %v, want %v", s.userID, uid)
	}
}
