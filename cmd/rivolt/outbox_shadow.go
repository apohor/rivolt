package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/outbox"
	"github.com/apohor/rivolt/internal/rivian"
)

// outboxShadowOn is the RIVOLT_OUTBOX_SHADOW switch (S19 8 slice 2): off unless "1". When on, every drive and charge close that fires its hooks
// also records one close_hook intent in the outbox. Nothing consumes it yet (no handler is registered), so no effect changes; the direct hooks stay
// the only path. Unset the variable to stop; the rows already written expire by Purge.
func outboxShadowOn() bool { return os.Getenv("RIVOLT_OUTBOX_SHADOW") == "1" }

// outboxShadow writes the shadow rows.
type outboxShadow struct {
	pool      *sql.DB
	store     *outbox.Store
	resolvers *db.VehicleResolverFactory
	logger    *slog.Logger
	errs      *prometheus.CounterVec
}

func newOutboxShadow(pool *sql.DB, store *outbox.Store, resolvers *db.VehicleResolverFactory, logger *slog.Logger, reg prometheus.Registerer) *outboxShadow {
	o := &outboxShadow{pool: pool, store: store, resolvers: resolvers, logger: logger,
		errs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rivolt_outbox_shadow_errors_total",
			Help: "Shadow close intents that could not be recorded, by type (drive, charge). The direct hook is unaffected."}, []string{"type"})}
	o.errs.WithLabelValues("drive").Add(0)
	o.errs.WithLabelValues("charge").Add(0)
	if reg != nil {
		reg.MustRegister(o.errs)
	}
	return o
}

// register adds the shadow hooks after every direct close hook (hooks run in order registered), so the insert never delays them.
func (o *outboxShadow) register(reg *rivian.MonitorRegistry) {
	reg.AddDriveCloseHookFactory(o.driveHook)
	reg.AddChargeCloseHookFactory(o.chargeHook)
}

func (o *outboxShadow) driveHook(uid uuid.UUID) rivian.DriveCloseHook {
	return func(ctx context.Context, d drives.Drive) { o.record(ctx, uid, d.VehicleID, "drive", "d", d.ID) }
}

func (o *outboxShadow) chargeHook(uid uuid.UUID) rivian.ChargeCloseHook {
	return func(ctx context.Context, c charges.Charge) { o.record(ctx, uid, c.VehicleID, "charge", "c", c.ID) }
}

// record writes one intent in its own bounded transaction, under the recorder's fence token when ctx carries one (a lost lease writes nothing).
// It never returns an error to the hook: a failure is logged and counted, and the direct path has already run.
func (o *outboxShadow) record(ctx context.Context, uid uuid.UUID, rivianVehicleID, typ, short, session string) {
	if session == "" || rivianVehicleID == "" {
		return
	}
	// The hook's own context may be nearly spent after the direct hooks; the insert has its own 2 s bound.
	ctx = context.WithoutCancel(ctx)
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	vid, err := o.resolvers.For(uid).Resolve(rctx, rivianVehicleID)
	if err == nil {
		payload, _ := json.Marshal(map[string]string{"session": session, "type": typ})
		in := outbox.Intent{Key: "close:" + vid.String() + ":" + short + ":" + session, UserID: uid, VehicleID: vid, Kind: outbox.KindCloseHook, Payload: payload}
		err = fence.InTx(rctx, o.pool, func(tctx context.Context, tx *sql.Tx) error {
			if tok, ok := fence.From(ctx); ok {
				var held bool
				if err := tx.QueryRowContext(tctx, `SELECT rivolt_lease_ok($1::uuid, $2::text, $3::bigint)`, tok.Group, tok.Pod, tok.Epoch).Scan(&held); err != nil {
					return err
				}
				if !held {
					return nil
				}
			}
			_, err := o.store.Insert(tctx, tx, in)
			return err
		})
	}
	if err != nil {
		o.errs.WithLabelValues(typ).Inc()
		o.logger.Warn("outbox shadow insert failed", "type", typ, "session", session, "err_kind", "insert")
	}
}
