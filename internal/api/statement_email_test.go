package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/statement"
)

type memStmStore struct {
	mu     sync.Mutex
	prefs  map[uuid.UUID]statement.Prefs
	emails map[uuid.UUID]string
}

func newMemStm() *memStmStore {
	return &memStmStore{prefs: map[uuid.UUID]statement.Prefs{}, emails: map[uuid.UUID]string{}}
}
func (m *memStmStore) GetPrefs(_ context.Context, u uuid.UUID) (statement.Prefs, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.prefs[u]; ok {
		return p, nil
	}
	return statement.Prefs{Timezone: "UTC"}, nil
}
func (m *memStmStore) SetPrefs(_ context.Context, u uuid.UUID, p statement.Prefs) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prefs[u] = p
	return nil
}
func (m *memStmStore) LastSentPeriod(context.Context, uuid.UUID) (string, error) {
	return "2026-09", nil
}
func (m *memStmStore) Email(_ context.Context, u uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emails[u], nil
}
func (m *memStmStore) OptedIn(context.Context) ([]statement.Candidate, error) { return nil, nil }
func (m *memStmStore) Claim(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	return false, nil
}
func (m *memStmStore) Mark(context.Context, uuid.UUID, string, string, string, time.Time) error {
	return nil
}
func (m *memStmStore) Purge(context.Context, time.Time) (int64, error) { return 0, nil }

func stmFixture(flagOn bool) (*statementAPI, *memStmStore, uuid.UUID, *time.Time) {
	store := newMemStm()
	uid := uuid.New()
	store.emails[uid] = "sam@example.com"
	signer := statement.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	nowp := &now
	a := &statementAPI{
		store: store, signer: signer,
		flag: func(string) bool { return flagOn },
		now:  func() time.Time { return *nowp },
		monthly: func(_ context.Context, _ uuid.UUID, period string, _ *time.Location) (ledger.Monthly, error) {
			return ledger.Monthly{Period: period, Currency: "USD",
				Spend:  ledger.Spend{Total: 50, PricedSessions: 2},
				Energy: ledger.Energy{Sessions: 2, KWhAdded: 80},
			}, nil
		},
		job: &statement.Job{Store: store, Signer: signer, BaseURL: "https://rivolt.dev", Now: func() time.Time { return *nowp }},
	}
	return a, store, uid, nowp
}

func stmDo(a *statementAPI, uid uuid.UUID, method, path, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/api/settings/statement-email", func(w http.ResponseWriter, req *http.Request) { a.getSettings(uid, w, req) })
	r.Put("/api/settings/statement-email", func(w http.ResponseWriter, req *http.Request) { a.putSettings(uid, w, req) })
	r.Get("/api/statement-email/preview", func(w http.ResponseWriter, req *http.Request) { a.preview(uid, w, req) })
	r.Get("/api/statement-email/unsubscribe", a.unsubscribeGet)
	r.Post("/api/statement-email/unsubscribe", a.unsubscribePost)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestStatementSettings_DefaultsOffAndFlagGatesAvailability(t *testing.T) {
	a, _, uid, _ := stmFixture(false)
	rec := stmDo(a, uid, "GET", "/api/settings/statement-email", "")
	var s statementSettings
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	if rec.Code != 200 || s.Enabled || s.Available || s.Timezone != "UTC" {
		t.Fatalf("flag off: %d %+v", rec.Code, s)
	}
	a2, _, uid2, _ := stmFixture(true)
	_ = json.Unmarshal(stmDo(a2, uid2, "GET", "/api/settings/statement-email", "").Body.Bytes(), &s)
	if s.Enabled || !s.Available || s.LastSentPeriod != "2026-09" {
		t.Fatalf("flag on: %+v", s)
	}
}

func TestStatementSettings_PutRules(t *testing.T) {
	a, store, uid, _ := stmFixture(true)
	put := func(body string) *httptest.ResponseRecorder {
		return stmDo(a, uid, "PUT", "/api/settings/statement-email", body)
	}
	if c := put(`{"enabled":true,"timezone":"Not/AZone"}`).Code; c != 400 {
		t.Errorf("bad tz = %d", c)
	}
	if c := put(`{"enabled":true,"timezone":"Local"}`).Code; c != 400 {
		t.Errorf("tz Local = %d", c)
	}
	if rec := put(`{"enabled":true,"timezone":"America/Chicago"}`); rec.Code != 200 {
		t.Fatalf("opt in = %d %s", rec.Code, rec.Body)
	}
	if p, _ := store.GetPrefs(context.Background(), uid); !p.Enabled || p.Timezone != "America/Chicago" {
		t.Fatalf("prefs = %+v", p)
	}
	// Missing timezone defaults to UTC.
	put(`{"enabled":true}`)
	if p, _ := store.GetPrefs(context.Background(), uid); p.Timezone != "UTC" {
		t.Fatalf("tz default = %q", p.Timezone)
	}
	// No email on the account: can't opt in (409), but can opt out.
	store.emails[uid] = ""
	if c := put(`{"enabled":true}`).Code; c != 409 {
		t.Errorf("no email opt-in = %d, want 409", c)
	}
	if c := put(`{"enabled":false}`).Code; c != 200 {
		t.Errorf("opt-out without email = %d", c)
	}
	// Operator flag off: opting in is a 404.
	a2, _, uid2, _ := stmFixture(false)
	if c := stmDo(a2, uid2, "PUT", "/api/settings/statement-email", `{"enabled":true}`).Code; c != 404 {
		t.Errorf("flag off opt-in = %d, want 404", c)
	}
}

func TestStatementPreview(t *testing.T) {
	a, store, uid, _ := stmFixture(true)
	get := func(q string) (int, map[string]any) {
		rec := stmDo(a, uid, "GET", "/api/statement-email/preview"+q, "")
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	code, m := get("")
	if code != 200 || m["will_send"] != false || m["reason"] != "not_opted_in" || !strings.Contains(m["subject"].(string), "September") {
		t.Fatalf("not opted in: %d %v", code, m)
	}
	_ = store.SetPrefs(context.Background(), uid, statement.Prefs{Enabled: true, Timezone: "UTC"})
	code, m = get("?month=2026-08")
	if code != 200 || m["will_send"] != true || !strings.Contains(m["subject"].(string), "August") || m["text"] == "" || m["html"] == "" {
		t.Fatalf("opted in: %d %v", code, m)
	}
	if c, _ := get("?month=2026-13"); c != 400 {
		t.Errorf("bad month = %d", c)
	}
	// Silent month: no message at all.
	a.monthly = func(_ context.Context, _ uuid.UUID, p string, _ *time.Location) (ledger.Monthly, error) {
		return ledger.Monthly{Period: p, Currency: "USD"}, nil
	}
	if _, m = get(""); m["will_send"] != false || m["reason"] != "silent_month" {
		t.Fatalf("silent: %v", m)
	}
	// Operator flag off: 404, nothing rendered.
	a.flag = func(string) bool { return false }
	if c, _ := get(""); c != 404 {
		t.Errorf("flag off preview = %d", c)
	}
}

func TestStatementUnsubscribe_GetShowsPostPerformsAndIsIdempotent(t *testing.T) {
	a, store, uid, now := stmFixture(true)
	_ = store.SetPrefs(context.Background(), uid, statement.Prefs{Enabled: true, Timezone: "America/Chicago"})
	tok := a.signer.Sign(uid, "2026-09", now.Add(statement.TokenTTL))
	path := "/api/statement-email/unsubscribe?t=" + tok

	// GET never changes anything (link scanners).
	rec := stmDo(a, uid, "GET", path, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<form method=\"post\"") {
		t.Fatalf("GET page: %d", rec.Code)
	}
	if p, _ := store.GetPrefs(context.Background(), uid); !p.Enabled {
		t.Fatal("GET unsubscribed the user")
	}
	// POST performs, with no session at all and the RFC 8058 body.
	rec = stmDo(a, uuid.Nil, "POST", path, "List-Unsubscribe=One-Click")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "unsubscribed") {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	p, _ := store.GetPrefs(context.Background(), uid)
	if p.Enabled || p.Timezone != "America/Chicago" {
		t.Fatalf("prefs after unsubscribe: %+v (timezone must be kept)", p)
	}
	if rec = stmDo(a, uuid.Nil, "POST", path, ""); rec.Code != 200 {
		t.Fatalf("second POST = %d (must be idempotent)", rec.Code)
	}
	for _, h := range []string{"Cache-Control", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing %s on the unsubscribe page", h)
		}
	}
}

func TestStatementUnsubscribe_RejectsTamperedExpiredAndOtherKeys(t *testing.T) {
	a, store, uid, now := stmFixture(true)
	_ = store.SetPrefs(context.Background(), uid, statement.Prefs{Enabled: true, Timezone: "UTC"})
	good := a.signer.Sign(uid, "2026-09", now.Add(statement.TokenTTL))
	expired := a.signer.Sign(uid, "2026-09", now.Add(-time.Second))
	foreign := statement.NewSigner([]byte("ffffffffffffffffffffffffffffffff")).Sign(uid, "2026-09", now.Add(statement.TokenTTL))
	for name, tok := range map[string]string{
		"tampered": good[:len(good)-2] + "AA", "expired": expired, "foreign key": foreign, "empty": "", "junk": "abc",
	} {
		for _, m := range []string{"GET", "POST"} {
			rec := stmDo(a, uuid.Nil, m, "/api/statement-email/unsubscribe?t="+tok, "")
			if rec.Code != 400 {
				t.Errorf("%s %s = %d, want 400", m, name, rec.Code)
			}
		}
	}
	if p, _ := store.GetPrefs(context.Background(), uid); !p.Enabled {
		t.Fatal("a rejected token changed the preference")
	}
}
