package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/vehiclediscovery"
)

// ownerDiscoveryJob is this pod's owner-mode discovery job (RIVOLT_OWNER_DISCOVERY), set before it starts; the forward runner's adopt nudges it so
// a user who has just signed in is looked up at once rather than at the next tick.
var ownerDiscoveryJob atomic.Pointer[vehiclediscovery.Job]

// startVehicleDiscovery runs the vehicle discovery job (internal/vehiclediscovery) on this pod; the job takes a Postgres advisory lock, so
// only one pod's pass does anything. It finds the linked users nobody records (a stored Rivian session, no vehicle row, so no group, no lease
// and no owner) and primes their vehicles with one lookup each. Single-owner M3-6.
func startVehicleDiscovery(ctx context.Context, logger *slog.Logger, appMetrics *metrics.Metrics, job *vehiclediscovery.Job) {
	for _, o := range []string{vehiclediscovery.OutcomeStored, vehiclediscovery.OutcomeEmpty, vehiclediscovery.OutcomeError, vehiclediscovery.OutcomeSkipped, vehiclediscovery.OutcomeNeedsReauth} {
		appMetrics.VehicleDiscoveryTotal.WithLabelValues(o).Add(0)
	}
	job.Observe = func(outcome string) { appMetrics.VehicleDiscoveryTotal.WithLabelValues(outcome).Inc() }
	job.Logger = logger
	go job.Run(ctx)
}

// ownerDiscoveryEnvError is the boot check for RIVOLT_OWNER_DISCOVERY=1: with no group leases nobody owns a group, so no user is ever looked up while
// /api/vehicles stops asking Rivian, and every vehicle-less user would wait at "looking" forever; without the forwarder the sign-in nudge never
// reaches the owner. The process refuses to start rather than run that way.
func ownerDiscoveryEnvError(getenv func(string) string) error {
	for _, v := range []string{"RIVOLT_GROUP_LEAD", "RIVOLT_FORWARD"} {
		if getenv(v) != "1" {
			return fmt.Errorf("RIVOLT_OWNER_DISCOVERY=1 needs %s=1; refusing to start", v)
		}
	}
	return nil
}

// discoveryLookup is the job's only Rivian call: the user's vehicle list, with the user's own session (restored from the secrets store when this
// pod's client has none, as the HTTP path's clientFor does). The account was built with the reauth sink, so a rejected session is flagged and
// the one-time email sent by the client itself.
func discoveryLookup(accounts rivian.AccountRegistry, store *secrets.Store) vehiclediscovery.Lookup {
	return func(ctx context.Context, uid uuid.UUID) ([]rivian.Vehicle, error) {
		a := accounts.For(uid)
		c, ok := a.(rivian.Client)
		if a == nil || !ok {
			return nil, errors.New("no Rivian client for the user")
		}
		if store != nil && !a.Authenticated() {
			// Detached from any request, so it carries the user explicitly: the secrets store is user-scoped under row-level security.
			sess, err := secrets.LoadRivianSession(secrets.WithSite(auth.WithUser(ctx, uid), "discovery"), store, uid)
			if err == nil && sess.UserSessionToken != "" {
				a.Restore(sess)
			}
		}
		return c.Vehicles(ctx)
	}
}
