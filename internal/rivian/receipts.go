package rivian

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Rivian's Charging history, as the owner app reads it. Two operations on
// the charging gateway:
//
//   - GetCompletedSessionSummaries: one row per completed session with the
//     billed total. It takes no arguments and returns the whole history, so
//     the first call per user is also the backfill.
//   - GetDetailedSessionInfoByTxnId: asked for the charger's name, vendor
//     and isHomeCharger ONLY. The app's document also selects the charger
//     address, the order and the payment-method card (brand, last four);
//     none of that is selected here, so none of it can reach us, be logged
//     or be stored. Do not add fields to these documents without the
//     privacy review (docs/product/privacy-policy.DRAFT.md data list).
//
// Both go through doGraphQLAt like every other call, so the kill switch,
// the breaker, the upstream bucket and the per-operation metrics label
// (the OperationName below) apply.

const qCompletedSessionSummaries = `query GetCompletedSessionSummaries {
  getCompletedSessionSummaries {
    transactionId startInstant endInstant totalCost rangeAddedKm totalEnergyKwh vendor currencyCode city
  }
}`

const qSessionCharger = `query GetDetailedSessionInfoByTxnId($transactionId: ID!) {
  getDetailedSessionInfoByTxnId(transactionId: $transactionId) {
    charger { name vendor isHomeCharger }
  }
}`

// SessionSummary is one row of Rivian's Charging history.
type SessionSummary struct {
	TransactionID string
	StartedAt     time.Time
	EndedAt       time.Time
	// TotalCost is what Rivian billed; nil when the session has no cost
	// (the app shows nothing for it). Currency is its ISO code.
	TotalCost    *float64
	Currency     string
	EnergyKWh    *float64
	RangeAddedKm *float64
	Vendor       string
	City         string
}

// SessionCharger is the little the detail call is allowed to return.
type SessionCharger struct {
	Name   string
	Vendor string
	// IsHome is Rivian's isHomeCharger; nil when not reported.
	IsHome *bool
}

// ReceiptSource is what the receipts sync needs from a Rivian account.
// *LiveClient implements it.
type ReceiptSource interface {
	CompletedSessionSummaries(ctx context.Context) ([]SessionSummary, error)
	SessionCharger(ctx context.Context, transactionID string) (*SessionCharger, error)
}

type completedSessionsData struct {
	GetCompletedSessionSummaries []struct {
		TransactionID string   `json:"transactionId"`
		StartInstant  string   `json:"startInstant"`
		EndInstant    string   `json:"endInstant"`
		TotalCost     *float64 `json:"totalCost"`
		RangeAddedKm  *float64 `json:"rangeAddedKm"`
		TotalEnergy   *float64 `json:"totalEnergyKwh"`
		Vendor        *string  `json:"vendor"`
		CurrencyCode  *string  `json:"currencyCode"`
		City          *string  `json:"city"`
	} `json:"getCompletedSessionSummaries"`
}

// CompletedSessionSummaries returns the user's completed charging sessions
// with what Rivian billed. Rows without a transaction id or a parseable
// window are dropped.
func (c *LiveClient) CompletedSessionSummaries(ctx context.Context) ([]SessionSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, ErrNotAuthenticated
	}
	data, err := doGraphQLAt[completedSessionsData](ctx, c, c.receiptsEndpoint(), graphQLRequest{
		OperationName: "GetCompletedSessionSummaries",
		Query:         qCompletedSessionSummaries,
		Variables:     struct{}{},
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("GetCompletedSessionSummaries: %w", err)
	}
	out := make([]SessionSummary, 0, len(data.GetCompletedSessionSummaries))
	for _, r := range data.GetCompletedSessionSummaries {
		start, e1 := parseInstant(r.StartInstant)
		end, e2 := parseInstant(r.EndInstant)
		if r.TransactionID == "" || e1 != nil || e2 != nil || !end.After(start) {
			continue
		}
		s := SessionSummary{
			TransactionID: r.TransactionID, StartedAt: start, EndedAt: end,
			TotalCost: r.TotalCost, EnergyKWh: r.TotalEnergy, RangeAddedKm: r.RangeAddedKm,
		}
		if r.CurrencyCode != nil {
			s.Currency = strings.ToUpper(strings.TrimSpace(*r.CurrencyCode))
		}
		if r.Vendor != nil {
			s.Vendor = strings.TrimSpace(*r.Vendor)
		}
		if r.City != nil {
			s.City = strings.TrimSpace(*r.City)
		}
		out = append(out, s)
	}
	return out, nil
}

type sessionChargerData struct {
	GetDetailedSessionInfoByTxnId *struct {
		Charger *struct {
			Name          *string `json:"name"`
			Vendor        *string `json:"vendor"`
			IsHomeCharger *bool   `json:"isHomeCharger"`
		} `json:"charger"`
	} `json:"getDetailedSessionInfoByTxnId"`
}

// SessionCharger returns the charger name, vendor and isHomeCharger for a
// transaction, or nil when Rivian has no charger for it.
func (c *LiveClient) SessionCharger(ctx context.Context, transactionID string) (*SessionCharger, error) {
	if strings.TrimSpace(transactionID) == "" {
		return nil, errors.New("rivian: transactionID is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, ErrNotAuthenticated
	}
	data, err := doGraphQLAt[sessionChargerData](ctx, c, c.receiptsEndpoint(), graphQLRequest{
		OperationName: "GetDetailedSessionInfoByTxnId",
		Query:         qSessionCharger,
		Variables:     map[string]any{"transactionId": transactionID},
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("GetDetailedSessionInfoByTxnId: %w", err)
	}
	d := data.GetDetailedSessionInfoByTxnId
	if d == nil || d.Charger == nil {
		return nil, nil
	}
	out := &SessionCharger{IsHome: d.Charger.IsHomeCharger}
	if d.Charger.Name != nil {
		out.Name = strings.TrimSpace(*d.Charger.Name)
	}
	if d.Charger.Vendor != nil {
		out.Vendor = strings.TrimSpace(*d.Charger.Vendor)
	}
	return out, nil
}

func (c *LiveClient) receiptsEndpoint() string {
	if c.chargingEndpoint != "" {
		return c.chargingEndpoint
	}
	return DefaultChargingEndpoint
}

func parseInstant(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty instant")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// QSessionChargerForTest exposes the detail document to the privacy test in
// internal/receipts.
func QSessionChargerForTest() string { return qSessionCharger }
