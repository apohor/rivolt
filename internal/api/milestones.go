package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/milestones"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/vehiclelease"
)

// MilestoneLedgerInput loads a vehicle's whole ledger input for the milestone
// checker: the same code path as the Money tab.
func MilestoneLedgerInput(pool *sql.DB, sf *settings.Factory) func(ctx context.Context, uid uuid.UUID, rivianID string) (ledger.Input, error) {
	la := newLedgerAPI(pool, sf)
	return func(ctx context.Context, uid uuid.UUID, rivianID string) (ledger.Input, error) {
		now := time.Now()
		return la.input(ctx, uid, ledgerParams{vehicleID: rivianID, tzSource: "milestones"},
			ledger.Period{Month: "all", Loc: time.UTC, Start: time.Unix(0, 0).UTC(), End: now.AddDate(0, 0, 1)})
	}
}

// MilestoneLease says how far through its lease a vehicle is (Total 0: none).
func MilestoneLease(pool *sql.DB) func(ctx context.Context, uid, vehicle uuid.UUID) milestones.LeaseTerm {
	st := vehiclelease.NewStore(pool)
	return func(ctx context.Context, uid, vehicle uuid.UUID) milestones.LeaseTerm {
		t, err := st.Get(ctx, uid, vehicle)
		if err != nil || t == nil {
			return milestones.LeaseTerm{}
		}
		out, err := vehiclelease.Compute(*t, time.Now(), nil, nil)
		if err != nil {
			return milestones.LeaseTerm{}
		}
		return milestones.LeaseTerm{Elapsed: out.DaysElapsed, Total: out.DaysTotal}
	}
}

type milestoneCard struct {
	VehicleID  string            `json:"vehicle_id"` // the Rivian vehicle id
	Key        string            `json:"key"`
	Kind       string            `json:"kind"`
	AchievedAt time.Time         `json:"achieved_at"`
	Title      string            `json:"title"`
	Body       string            `json:"body"`
	Detail     milestones.Detail `json:"detail"`
}

type milestonesAPI struct {
	store *milestones.PGStore
	now   func() time.Time
}

// GET /api/milestones: the account's cards, achieved in the last 30 days and not
// dismissed, newest first. Cards only exist for milestones passed after the
// feature shipped.
func (a *milestonesAPI) list(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.List(r.Context(), uid, a.now().Add(-milestones.CardWindow))
	if err != nil {
		serverError(w, r, "milestones", err)
		return
	}
	out := make([]milestoneCard, 0, len(rows))
	for _, m := range rows {
		title, body := milestones.Text(m.Kind, m.Detail)
		out = append(out, milestoneCard{VehicleID: m.RivianID, Key: m.Key, Kind: m.Kind, AchievedAt: m.AchievedAt, Title: title, Body: body, Detail: m.Detail})
	}
	writeJSON(w, http.StatusOK, map[string]any{"milestones": out})
}

// POST /api/milestones/dismiss {vehicle_id, key}: hides a card. Idempotent; 404
// when the account has no such card. Not an admin's act while viewing as the user.
func (a *milestonesAPI) dismiss(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	if auth.IsImpersonating(r.Context()) {
		http.Error(w, "not available while viewing as another user", http.StatusForbidden)
		return
	}
	var body struct {
		VehicleID string `json:"vehicle_id"`
		Key       string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.VehicleID == "" || body.Key == "" {
		http.Error(w, "vehicle_id and key required", http.StatusBadRequest)
		return
	}
	ok, err := a.store.Dismiss(r.Context(), uid, body.VehicleID, body.Key, a.now())
	if err != nil {
		serverError(w, r, "milestones dismiss", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
