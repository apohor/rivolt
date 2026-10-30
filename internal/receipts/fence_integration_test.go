//go:build integration

package receipts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// The receipts writes carry the group lease when the context has a token: the owner's claim and
// writes go through, a claim inside the interval is "not claimed" (not a refusal), and a stale
// epoch, another pod or a missing lease is refused with nothing changed. Without a token they run
// as before.
func TestReceiptsWrites_FencedByTheGroupLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "receiptsfence")
	if err != nil {
		t.Fatal(err)
	}
	st := receipts.NewStore(pool)

	var epoch int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	owner := fence.With(ctx, fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epoch})

	// The owner claims; a second claim within the interval is not claimed, and not an error.
	if ok, err := st.ClaimSummaries(owner, uid, time.Hour); err != nil || !ok {
		t.Fatalf("owner claim = %v, %v; want claimed", ok, err)
	}
	if ok, err := st.ClaimSummaries(owner, uid, time.Hour); err != nil || ok {
		t.Fatalf("second claim within the interval = %v, %v; want not claimed, no error", ok, err)
	}
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	sums := []rivian.SessionSummary{
		{TransactionID: "txn-1", StartedAt: start, EndedAt: start.Add(30 * time.Minute), TotalCost: ptr(12.5), Currency: "USD", EnergyKWh: ptr(40), Vendor: "ChargePoint", City: "Somewhere"},
	}
	if err := st.UpsertReceipts(owner, uid, sums); err != nil {
		t.Fatalf("owner upsert: %v", err)
	}
	if err := st.SaveDetail(owner, uid, "txn-1", &rivian.SessionCharger{Name: "Station", Vendor: "ChargePoint"}); err != nil {
		t.Fatalf("owner detail: %v", err)
	}
	if err := st.RefreshMatched(owner, uid); err != nil {
		t.Fatalf("owner refresh: %v", err)
	}
	if err := st.MarkResult(owner, uid, nil); err != nil {
		t.Fatalf("owner mark: %v", err)
	}
	if got, err := st.ListAll(ctx, uid); err != nil || len(got) != 1 || got[0].ChargerName != "Station" {
		t.Fatalf("after the owner's writes: %+v, %v", got, err)
	}

	// Refused: a stale epoch, another pod, a group with no lease row. Nothing changes.
	stale := []fence.Token{
		{Group: uid.String(), Pod: "pod-a", Epoch: epoch - 1},
		{Group: uid.String(), Pod: "pod-b", Epoch: epoch},
		{Group: uuid.NewString(), Pod: "pod-a", Epoch: epoch},
	}
	for i, tok := range stale {
		bad := fence.With(ctx, tok)
		more := []rivian.SessionSummary{{TransactionID: "txn-stale", StartedAt: start, EndedAt: start.Add(time.Hour), Currency: "USD"}}
		for name, err := range map[string]error{
			"claim":   func() error { _, err := st.ClaimDetails(bad, uid, 0); return err }(),
			"upsert":  st.UpsertReceipts(bad, uid, more),
			"detail":  st.SaveDetail(bad, uid, "txn-1", &rivian.SessionCharger{Name: "Changed"}),
			"refresh": st.RefreshMatched(bad, uid),
			"mark":    st.MarkResult(bad, uid, errors.New("x")),
			"apply":   st.Apply(bad, uid, []receipts.Pair{{ReceiptID: uuid.New(), ChargeID: uuid.New()}}),
		} {
			if !errors.Is(err, fence.ErrFenced) {
				t.Errorf("token %d, %s: %v; want fence.ErrFenced", i, name, err)
			}
		}
	}
	if got, err := st.ListAll(ctx, uid); err != nil || len(got) != 1 || got[0].ChargerName != "Station" {
		t.Errorf("refused writes changed something: %+v, %v", got, err)
	}

	// Without a token: the plain path, as before.
	if err := st.UpsertReceipts(ctx, uid, []rivian.SessionSummary{{TransactionID: "txn-2", StartedAt: start, EndedAt: start.Add(time.Hour), Currency: "USD"}}); err != nil {
		t.Fatalf("plain upsert: %v", err)
	}
	if got, _ := st.ListAll(ctx, uid); len(got) != 2 {
		t.Errorf("plain upsert: %d receipts, want 2", len(got))
	}
}

func ptr(f float64) *float64 { return &f }
