package api

import (
	"database/sql"

	"github.com/apohor/rivolt/internal/aibudget"
	"github.com/apohor/rivolt/internal/charges"
	rivoltcrypto "github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
)

// RequestStores are the per-user stores request handling uses (row-level security, stage 2): every one of them reads and writes the
// caller's own rows. main and the pool-on integration tests both build them here, from the request pool req and the system pool
// sys, so what CI exercises is what production wires; a store added or moved is changed in one place.
//
// sys is for the few statements in these stores that must span users or are DDL, each commented where it is used: APNs token
// registration (push) and a month's partition (samples).
type RequestStores struct {
	Resolver   *db.VehicleResolverFactory
	Drives     *drives.Factory
	Charges    *charges.Factory
	Samples    *samples.Factory
	Settings   *settings.Factory
	Push       *push.Factory
	Trips      *trips.Factory
	PackHealth *packhealth.Store
	AIBudget   *aibudget.Store
	// Secrets is nil when there is no sealer (the Rivian sign-in surface is then read-only).
	Secrets *secrets.Store
}

// NewRequestStores builds the request-side stores on req, with sys for their cross-user and DDL statements. sealer may be nil.
func NewRequestStores(req, sys *sql.DB, sealer rivoltcrypto.Sealer) RequestStores {
	rf := db.NewVehicleResolverFactory(req)
	rs := RequestStores{
		Resolver:   rf,
		Drives:     drives.NewFactory(req, rf),
		Charges:    charges.NewFactory(req, rf),
		Samples:    samples.NewFactoryWithSystem(req, sys, rf), // a month's partition is DDL: system pool
		Settings:   settings.NewFactory(req),
		Push:       push.NewFactoryWithSystem(req, sys), // APNs token registration spans users: system pool for those statements
		Trips:      trips.NewFactory(req),
		PackHealth: packhealth.NewStore(req),
		AIBudget:   aibudget.New(req),
	}
	if sealer != nil {
		rs.Secrets = secrets.New(req, sealer)
	}
	return rs
}
