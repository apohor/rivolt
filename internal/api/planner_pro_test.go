package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/trips"
)

// plannerDB answers the two reads of the entitlements service: no entitlement rows, and the account's created_at from a map.
type plannerDB struct{ created map[string]time.Time }

func (d plannerDB) Connect(context.Context) (driver.Conn, error) { return plannerConn(d), nil }
func (d plannerDB) Driver() driver.Driver                        { return nil }

type plannerConn plannerDB

func (c plannerConn) Prepare(q string) (driver.Stmt, error) { return plannerStmt{c, q}, nil }
func (plannerConn) Close() error                            { return nil }
func (plannerConn) Begin() (driver.Tx, error)               { return nil, errors.New("no tx") }

type plannerStmt struct {
	c plannerConn
	q string
}

func (plannerStmt) Close() error  { return nil }
func (plannerStmt) NumInput() int { return 1 }
func (plannerStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errors.New("no exec")
}
func (s plannerStmt) Query(args []driver.Value) (driver.Rows, error) {
	if strings.Contains(s.q, "FROM users") {
		id, _ := args[0].(string)
		return &plannerRows{cols: []string{"created_at"}, rows: [][]driver.Value{{s.c.created[id]}}}, nil
	}
	return &plannerRows{cols: []string{"source", "status", "current_period_end", "updated_at", "cancel_at_period_end"}}, nil
}

type plannerRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *plannerRows) Columns() []string { return r.cols }
func (r *plannerRows) Close() error      { return nil }
func (r *plannerRows) Next(dst []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dst, r.rows[r.i])
	r.i++
	return nil
}

// Trip planning is Pro for a free account created at or after the cutoff; the grandfathered account, the demo and an unset cutoff are open,
// and the 402 is the existing pro_required body.
func TestPlannerProMiddleware(t *testing.T) {
	cut := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	old, fresh, demo := uuid.New(), uuid.New(), uuid.New()
	db := sql.OpenDB(plannerDB{created: map[string]time.Time{old.String(): cut.Add(-48 * time.Hour), fresh.String(): cut.Add(48 * time.Hour)}})
	defer db.Close()
	on := entitlements.New(db, entitlements.Config{Billing: true, Demo: demo, PlannerProFrom: cut})
	gateOff := entitlements.New(db, entitlements.Config{Billing: true, Demo: demo})
	for _, tc := range []struct {
		name string
		e    *entitlements.Service
		uid  uuid.UUID
		want int
	}{
		{"new free account", on, fresh, http.StatusPaymentRequired},
		{"grandfathered free account", on, old, http.StatusOK},
		{"demo", on, demo, http.StatusOK},
		{"cutoff unset, new account", gateOff, fresh, http.StatusOK},
		{"no service", nil, fresh, http.StatusOK},
	} {
		reached := false
		h := plannerProMiddleware(tc.e)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		req := httptest.NewRequest(http.MethodPost, "/api/trips/plan", nil).WithContext(auth.WithUser(context.Background(), tc.uid))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		code := rec.Code
		if reached {
			code = http.StatusOK
		}
		if code != tc.want || reached != (tc.want == http.StatusOK) {
			t.Errorf("%s: %d reached=%v, want %d", tc.name, rec.Code, reached, tc.want)
		}
		if tc.want == http.StatusPaymentRequired && !strings.Contains(rec.Body.String(), `"pro_required"`) {
			t.Errorf("%s: body %s", tc.name, rec.Body.String())
		}
	}

	// A lapsed account keeps what it saved: every trip is flagged read-only (still listed and deletable), none is editable.
	list := []trips.SavedTrip{{}, {}, {}, {}}
	markReadOnlyTrips(context.Background(), on, fresh, list)
	for i, tr := range list {
		if !tr.ReadOnly {
			t.Errorf("trip %d of a planner-gated account is editable", i)
		}
	}
	list = []trips.SavedTrip{{}, {}, {}, {}}
	markReadOnlyTrips(context.Background(), on, old, list)
	if list[0].ReadOnly || list[2].ReadOnly || !list[3].ReadOnly {
		t.Errorf("grandfathered account: want only the 4th read-only, got %+v", list)
	}
}
