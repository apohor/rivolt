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

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/settings"
)

func TestPatchChargePricing_NetworkSlug(t *testing.T) {
	slug := dcfcrates.Networks[0].Slug
	name := dcfcrates.Networks[0].DisplayName
	rate := 0.52 // the user's own rate for the network

	run := func(f *fakePricer, ov []dcfcrates.NetworkOverride, body string) int {
		r := chi.NewRouter()
		r.Patch("/charges/{id}/pricing", patchChargePricing(f, func(context.Context) []dcfcrates.NetworkOverride { return ov }))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/charges/c-1/pricing", strings.NewReader(body)))
		return rec.Code
	}

	// The user's own saved rate prices the session.
	f := &fakePricer{found: true}
	own := []dcfcrates.NetworkOverride{{Slug: slug, GuestRate: rate}}
	if code := run(f, own, `{"network_slug":"`+slug+`"}`); code != http.StatusNoContent {
		t.Fatalf("slug = %d", code)
	}
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "network:c-1:"+name+":USD:") || !strings.HasSuffix(f.calls[0], ":0.52") {
		t.Errorf("calls = %v, want SetNetworkPrice at the user's rate %.2f", f.calls, rate)
	}

	// No rate of the user's: the table's dated rate is NOT applied; the answer says so.
	f = &fakePricer{found: true}
	if code := run(f, nil, `{"network_slug":"`+slug+`"}`); code != http.StatusBadRequest || len(f.calls) != 0 {
		t.Errorf("no user rate = %d, calls %v, want 400 and nothing written", code, f.calls)
	}

	// Member rate wins when the user's membership is active.
	f = &fakePricer{found: true}
	ov := []dcfcrates.NetworkOverride{{Slug: slug, GuestRate: 0.60, MemberRate: 0.35, MemberActive: true}}
	if code := run(f, ov, `{"network_slug":"`+slug+`"}`); code != http.StatusNoContent || !strings.HasSuffix(f.calls[0], ":0.35") {
		t.Errorf("member = %d %v, want 0.35", code, f.calls)
	}

	// Unknown slug, combined fields, and a missing charge.
	f = &fakePricer{found: true}
	if code := run(f, nil, `{"network_slug":"nope"}`); code != http.StatusBadRequest {
		t.Errorf("unknown slug = %d, want 400", code)
	}
	if code := run(f, nil, `{"network_slug":"`+slug+`","cost":5}`); code != http.StatusBadRequest {
		t.Errorf("slug+cost = %d, want 400", code)
	}
	if len(f.calls) != 0 {
		t.Errorf("rejected requests wrote: %v", f.calls)
	}
	if code := run(&fakePricer{}, nil, `{"network_slug":"`+slug+`"}`); code != http.StatusNotFound {
		t.Errorf("missing charge = %d, want 404", code)
	}
}

type fakeConfirmer struct {
	row     *charges.Charge
	confirm int64
}

func (f *fakeConfirmer) ConfirmNetwork(context.Context, string) (int64, error) {
	if f.row != nil && f.confirm > 0 {
		f.row.NetworkState = "confirmed"
	}
	return f.confirm, nil
}

func (f *fakeConfirmer) GetByExternalID(context.Context, string) (*charges.Charge, error) {
	return f.row, nil
}

func confirmCall(f *fakeConfirmer) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/charges/{id}/pricing/confirm", handleConfirmChargeNetwork(f, nil))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/charges/c-1/pricing/confirm", nil))
	return rec
}

func TestConfirmChargeNetwork(t *testing.T) {
	conf := 0.7
	row := func(state, src string) *charges.Charge {
		return &charges.Charge{ID: "c-1", CostSource: src, NetworkState: state, NetworkConfidence: &conf,
			NetworkEvidence: json.RawMessage(`{"v":1}`), StartedAt: time.Now(), EndedAt: time.Now()}
	}
	rec := confirmCall(&fakeConfirmer{row: row("needs_confirmation", "network_estimate"), confirm: 1})
	if rec.Code != 200 {
		t.Fatalf("confirm = %d: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["network_state"] != "confirmed" || got["network_confidence"] != 0.7 || got["network_evidence"] == nil {
		t.Errorf("body = %v", got)
	}
	// Idempotent: already confirmed is a 200 no-op (also after the
	// user picked the network themselves).
	for _, src := range []string{"network_estimate", "user"} {
		if rec := confirmCall(&fakeConfirmer{row: row("confirmed", src)}); rec.Code != 200 {
			t.Errorf("repeat (%s) = %d, want 200", src, rec.Code)
		}
	}
	// User-priced / nothing to confirm: 409. Unknown charge: 404.
	if rec := confirmCall(&fakeConfirmer{row: row("", "user")}); rec.Code != http.StatusConflict {
		t.Errorf("user-priced = %d, want 409", rec.Code)
	}
	if rec := confirmCall(&fakeConfirmer{}); rec.Code != http.StatusNotFound {
		t.Errorf("missing = %d, want 404", rec.Code)
	}
}

func TestDecorateChargeNetworkFieldsNull(t *testing.T) {
	b, _ := json.Marshal(decorateCharge(charges.Charge{ID: "x"}, settings.ChargingConfig{}, nil))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"network_state", "network_confidence", "network_evidence"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
}

func TestLedger_SummaryToConfirmCounts(t *testing.T) {
	uid := uuid.New()
	la := (&fakeLedgerData{}).api(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	la.toConfirmCount = func(context.Context, uuid.UUID) (int, int, error) { return 5, 2, nil }
	h := ledgerRouter(map[string]bool{uid.String(): true}, la)
	rec := ledgerGET(t, h, uid, "/api/ledger/summary?months=1")
	var sum ledgerSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.ToConfirmCount != 5 || sum.NeedsAttentionCount != 2 {
		t.Errorf("counts = %d/%d, want 5/2", sum.ToConfirmCount, sum.NeedsAttentionCount)
	}
}

func TestLedgerReceiptCarriesNetworkState(t *testing.T) {
	conf := 0.5
	started := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	kwh, peak := 40.0, 150.0
	cs := []ledger.Charge{{
		ID: "c-1", VehicleID: "v", StartedAt: started, EndedAt: started.Add(30 * time.Minute),
		EnergyKWh: &kwh, PeakKW: &peak, CostSource: "network_estimate", Network: "Tesla Supercharger",
		NetworkState: "needs_confirmation", NetworkConfidence: &conf,
		NetworkEvidence: json.RawMessage(`{"v":1,"source":"archive"}`),
	}}
	p, _ := ledger.ParsePeriod("2026-09", time.UTC)
	m := ledger.Build(ledger.Input{Charges: cs, Config: ledger.Config{Currency: "USD"}, Now: started}, p, true)
	if len(m.Receipts) != 1 {
		t.Fatalf("receipts = %d", len(m.Receipts))
	}
	r := m.Receipts[0]
	if r.NetworkState == nil || *r.NetworkState != "needs_confirmation" || r.NetworkConfidence == nil ||
		!strings.Contains(string(r.NetworkEvidence), "archive") || len(r.Sessions) != 1 ||
		r.Sessions[0].NetworkState == nil {
		t.Errorf("receipt = %+v", r)
	}
}

type fakeApplier struct {
	calls []string
	n     int64
}

func (f *fakeApplier) ApplyNetworkRate(_ context.Context, slug, network, currency string, rate float64) (int64, error) {
	f.calls = append(f.calls, slug+"|"+network+"|"+currency+"|"+strings.TrimRight(strings.TrimRight(strconvF(rate), "0"), "."))
	return f.n, nil
}

func strconvF(v float64) string { return strings.TrimSpace(strings.Replace(string(mustJSON(v)), "\"", "", -1)) }

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// "Apply my rate to past sessions" applies only a rate of the user's own,
// never a built-in network's table figure.
func TestApplyNetworkRate(t *testing.T) {
	slug := dcfcrates.Networks[0].Slug
	name := dcfcrates.Networks[0].DisplayName
	run := func(f *fakeApplier, ov []dcfcrates.NetworkOverride, body string) (int, string) {
		rec := httptest.NewRecorder()
		applyNetworkRate(f, func(context.Context) []dcfcrates.NetworkOverride { return ov })(rec, httptest.NewRequest(http.MethodPost, "/charges/apply-rate", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}

	f := &fakeApplier{n: 3}
	code, out := run(f, []dcfcrates.NetworkOverride{{Slug: slug, GuestRate: 0.55}}, `{"network_slug":"`+slug+`"}`)
	if code != http.StatusOK || !strings.Contains(out, `"priced":3`) || !strings.Contains(out, name) {
		t.Fatalf("apply = %d %s", code, out)
	}
	if len(f.calls) != 1 || f.calls[0] != slug+"|"+name+"|USD|0.55" {
		t.Errorf("calls = %v, want the user's 0.55 for %s", f.calls, name)
	}

	// Member rate wins when the membership is active, as for one session.
	f = &fakeApplier{}
	run(f, []dcfcrates.NetworkOverride{{Slug: slug, GuestRate: 0.60, MemberRate: 0.35, MemberActive: true}}, `{"network_slug":"`+slug+`"}`)
	if len(f.calls) != 1 || !strings.HasSuffix(f.calls[0], "|0.35") {
		t.Errorf("member calls = %v", f.calls)
	}

	// No rate of theirs (no row, or a row left at 0), unknown or empty slug:
	// 400 and nothing written.
	for name, tc := range map[string]struct {
		ov   []dcfcrates.NetworkOverride
		body string
	}{
		"no row":      {nil, `{"network_slug":"` + slug + `"}`},
		"row at zero": {[]dcfcrates.NetworkOverride{{Slug: slug}}, `{"network_slug":"` + slug + `"}`},
		"unknown":     {[]dcfcrates.NetworkOverride{{Slug: "nope", GuestRate: 0.5}}, `{"network_slug":"nope"}`},
		"empty":       {nil, `{"network_slug":""}`},
		"bad json":    {nil, `{`},
	} {
		f = &fakeApplier{}
		if code, _ := run(f, tc.ov, tc.body); code != http.StatusBadRequest || len(f.calls) != 0 {
			t.Errorf("%s: code %d calls %v, want 400 and no write", name, code, f.calls)
		}
	}
}
