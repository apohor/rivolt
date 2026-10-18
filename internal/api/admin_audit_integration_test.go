//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/adminaudit"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// Admin writes are recorded before they run, finished with the outcome, carry
// the real client address and only safe details, and are refused when the
// audit row cannot be written; the listing pages through them; impersonation
// start is recorded once per pair; old rows are purged.
func TestAdminAuditEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn := crossTenantDSN(ctx, t)
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	admin, err := db.EnsureUser(ctx, pool, "audit-admin")
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.EnsureUser(ctx, pool, "audit-target")
	if err != nil {
		t.Fatal(err)
	}
	store := adminaudit.New(pool)
	audit := adminAudit(store)

	ran := 0
	handler := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { ran++; w.WriteHeader(status) }
	}
	r := chi.NewRouter()
	r.With(audit(auditSpec{action: "user.set_role", targetType: "user", idParam: "id", userTarget: true, keys: []string{"role"}})).
		Post("/users/{id}/role", handler(200))
	r.With(audit(auditSpec{action: "user.set_disabled", targetType: "user", idParam: "id", userTarget: true, keys: []string{"disabled"}})).
		Post("/users/{id}/disabled", handler(409))
	r.With(audit(auditSpec{action: "settings.ai", targetType: "settings", targetID: "ai", fields: true})).
		Put("/settings/ai", handler(200))
	r.With(audit(auditSpec{action: "user.create", targetType: "user", usernameID: "username", keys: []string{"role"}})).
		Post("/users", handler(201))
	r.With(audit(auditSpec{action: "flag.signup_cap", targetType: "flag", targetID: "signup_cap", keys: []string{"limit"},
		before: func() map[string]any { return map[string]any{"limit": 25} }})).
		Put("/signup-cap", handler(200))

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(auth.WithUser(ctx, admin))
		req.RemoteAddr = "203.0.113.77:5555" // the real visitor, after the client-address middleware
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	do("POST", "/users/"+target.String()+"/role", `{"role":"admin"}`)
	do("POST", "/users/"+target.String()+"/disabled", `{"disabled":true}`) // the handler answers 409
	do("PUT", "/settings/ai", `{"provider":"anthropic","anthropic_api_key":"sk-ant-SECRET-VALUE","reason":"free text with someone@example.com"}`)
	do("POST", "/users", `{"username":"newperson@example.com","role":"user","email":"newperson@example.com","display_name":"New Person"}`)
	do("PUT", "/signup-cap", `{"limit":100}`)
	if ran != 5 {
		t.Fatalf("handlers ran %d times, want 5", ran)
	}

	rows, next, err := store.List(ctx, adminaudit.Filter{Limit: 100})
	if err != nil || next != 0 || len(rows) != 5 {
		t.Fatalf("list = %d rows next %d err %v", len(rows), next, err)
	}
	byAction := map[string]adminaudit.Row{}
	for _, row := range rows {
		byAction[row.Action] = row
		if row.Actor != admin || row.IP != "203.0.113.77" || row.Outcome == nil {
			t.Errorf("%s: actor %v ip %q outcome %v", row.Action, row.Actor, row.IP, row.Outcome)
		}
	}
	if role := byAction["user.set_role"]; role.TargetUser == nil || *role.TargetUser != target || role.Details["role"] != "admin" || *role.Outcome != "ok" || *role.Status != 200 {
		t.Errorf("set_role row = %+v", role)
	}
	if dis := byAction["user.set_disabled"]; *dis.Outcome != "failed" || *dis.Status != 409 || dis.Details["disabled"] != true {
		t.Errorf("a refused write must still be recorded as failed: %+v", dis)
	}
	if cap := byAction["flag.signup_cap"]; cap.Details["limit"] != float64(100) || cap.Details["before_limit"] != float64(25) {
		t.Errorf("signup_cap before/after = %v", cap.Details)
	}
	if c := byAction["user.create"]; c.TargetUser == nil || *c.TargetUser != db.UserIDFor("newperson@example.com") {
		t.Errorf("create row target = %+v", c)
	}

	// Nothing secret or personal reached the table.
	var everything string
	if err := pool.QueryRowContext(ctx, `SELECT string_agg(row_to_json(a)::text, ' ') FROM admin_audit a`).Scan(&everything); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SECRET", "sk-ant", "someone@example.com", "newperson", "New Person", "free text"} {
		if strings.Contains(everything, leak) {
			t.Errorf("audit rows contain %q: %s", leak, everything)
		}
	}
	if ai := byAction["settings.ai"]; !strings.Contains(strings.Join(toStrings(ai.Details["fields"]), ","), "anthropic_api_key") {
		t.Errorf("settings.ai should list the field NAMES: %v", ai.Details)
	}

	// Paging and filters.
	page1, next1, _ := store.List(ctx, adminaudit.Filter{Limit: 2})
	page2, next2, _ := store.List(ctx, adminaudit.Filter{Limit: 2, Before: next1})
	if len(page1) != 2 || next1 == 0 || len(page2) != 2 || next2 == 0 || page2[0].ID >= page1[1].ID {
		t.Errorf("paging: %d/%d next %d/%d", len(page1), len(page2), next1, next2)
	}
	if byUser, _, _ := store.List(ctx, adminaudit.Filter{TargetUser: target}); len(byUser) != 2 {
		t.Errorf("rows for the target user = %d, want 2", len(byUser))
	}
	if byAct, _, _ := store.List(ctx, adminaudit.Filter{Action: "flag.signup_cap"}); len(byAct) != 1 {
		t.Errorf("rows for the action = %d", len(byAct))
	}

	// The HTTP listing.
	rec := httptest.NewRecorder()
	handleAdminAuditList(store)(rec, httptest.NewRequest("GET", "/api/admin/audit?limit=3&action=user.set_role", nil))
	var out struct {
		Items      []map[string]any `json:"items"`
		NextBefore any              `json:"next_before"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Items) != 1 || out.NextBefore != nil {
		t.Errorf("listing = %d %s", rec.Code, rec.Body.String())
	}
	if rec := httptest.NewRecorder(); func() int {
		handleAdminAuditList(store)(rec, httptest.NewRequest("GET", "/api/admin/audit?actor=nope", nil))
		return rec.Code
	}() != 400 {
		t.Error("a bad actor id should be a 400")
	}

	// Impersonation start: once per pair per window.
	for i := 0; i < 3; i++ {
		if err := store.NoteImpersonationStart(ctx, admin, target, "203.0.113.77", 30*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	other := uuid.New()
	_ = store.NoteImpersonationStart(ctx, admin, other, "203.0.113.77", 30*time.Minute)
	if starts, _, _ := store.List(ctx, adminaudit.Filter{Action: "impersonation.start"}); len(starts) != 2 {
		t.Errorf("impersonation.start rows = %d, want 2 (one per target)", len(starts))
	}

	// Fail closed: a store that cannot write refuses the change and runs nothing.
	dead, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_ = dead.Close()
	rdead := chi.NewRouter()
	rdead.With(adminAudit(adminaudit.New(dead))(auditSpec{action: "user.delete", targetType: "user", idParam: "id", userTarget: true})).
		Delete("/users/{id}", handler(200))
	before := ran
	req := httptest.NewRequest("DELETE", "/users/"+target.String(), nil).WithContext(auth.WithUser(ctx, admin))
	rec = httptest.NewRecorder()
	rdead.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || ran != before {
		t.Errorf("unwritable audit: status %d, handler ran %v; want 503 and not run", rec.Code, ran != before)
	}

	// Purge: only rows past the retention window go.
	if _, err := pool.ExecContext(ctx, `UPDATE admin_audit SET at = now() - interval '3 years' WHERE action = 'user.create'`); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Purge(ctx); err != nil || n != 1 {
		t.Errorf("purged %d (%v), want 1", n, err)
	}
}

func toStrings(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
