package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// forwardRunner runs the forwarded kinds on this pod, the owner of the group. adopt (M3-1), fresh_state (M3-2b), plan_legs (M3-4) and ota_notes (M3-4b)
// exist, and nothing sends them before then.
type forwardRunner struct {
	store    *leases.Store
	secrets  *secrets.Store
	accounts rivian.AccountRegistry
	monitors *rivian.MonitorRegistry
	logger   *slog.Logger
}

func (r *forwardRunner) Run(ctx context.Context, kind forward.Kind, user, group string, args any) (forward.Result, error) {
	switch kind {
	case forward.KindAdopt:
		return r.adopt(ctx, user)
	case forward.KindFreshState:
		return r.freshState(ctx, user, group, args)
	case forward.KindPlanLegs:
		return r.planLegs(ctx, user, group, args)
	case forward.KindOTANotes:
		return r.otaNotes(ctx, user, group, args)
	}
	return forward.Result{Status: 501, Class: "not_implemented"}, nil
}

// otaNotes serves one release-notes read for a requester that does not hold the group. The vehicle must belong to the named group.
func (r *forwardRunner) otaNotes(ctx context.Context, user, group string, args any) (forward.Result, error) {
	a, ok := args.(*forward.OTANotesArgs)
	uid, err := uuid.Parse(user)
	if !ok || err != nil || r.accounts == nil || r.store == nil {
		return forward.Result{Status: 400, Class: "bad_request"}, nil
	}
	if g, known := r.store.GroupOfVehicle(a.VehicleID); !known || g != group {
		return forward.Result{Status: 404, Class: "not_found", Body: json.RawMessage(`{}`)}, nil
	}
	var c rivian.Client
	if acct := r.accounts.For(uid); acct != nil {
		c, _ = acct.(rivian.Client)
	}
	return api.ServeOTANotes(ctx, c, a.VehicleID), nil
}

// planLegs serves one planner call for a requester that does not hold the group, against the user's own client. The vehicle must belong
// to the named group, as for fresh_state.
func (r *forwardRunner) planLegs(ctx context.Context, user, group string, args any) (forward.Result, error) {
	a, ok := args.(*forward.PlanLegsArgs)
	uid, err := uuid.Parse(user)
	if !ok || err != nil || r.accounts == nil || r.store == nil {
		return forward.Result{Status: 400, Class: "bad_request"}, nil
	}
	if g, known := r.store.GroupOfVehicle(a.VehicleID); !known || g != group {
		return forward.Result{Status: 404, Class: "not_found", Body: json.RawMessage(`{}`)}, nil
	}
	var c rivian.Client
	if acct := r.accounts.For(uid); acct != nil {
		c, _ = acct.(rivian.Client)
	}
	return api.ServePlanLegs(ctx, c, api.PlanTripInputOf(a)), nil
}

// freshState serves one vehicle-state fetch for a requester that does not hold the group: the same fetch the handler makes, answered from
// this pod's cache when it is fresh enough. The vehicle must belong to the group the request names, so a request cannot make this pod
// call Rivian for a vehicle it does not own.
func (r *forwardRunner) freshState(ctx context.Context, user, group string, args any) (forward.Result, error) {
	a, ok := args.(*forward.FreshStateArgs)
	uid, err := uuid.Parse(user)
	if !ok || err != nil || r.accounts == nil || r.store == nil {
		return forward.Result{Status: 400, Class: "bad_request"}, nil
	}
	if g, known := r.store.GroupOfVehicle(a.VehicleID); !known || g != group {
		return forward.Result{Status: 404, Class: "not_found", Body: json.RawMessage(`{}`)}, nil
	}
	var c rivian.Client
	if acct := r.accounts.For(uid); acct != nil {
		c, _ = acct.(rivian.Client)
	}
	var hasSession func(context.Context) (bool, error)
	if r.secrets != nil {
		hasSession = func(ctx context.Context) (bool, error) {
			sess, err := secrets.LoadRivianSession(ctx, r.secrets, uid)
			return sess.UserSessionToken != "", err
		}
	}
	var mon *rivian.StateMonitor
	if r.monitors != nil {
		mon = r.monitors.For(uid)
	}
	return api.ServeFreshState(ctx, c, mon, hasSession, a.VehicleID, time.Duration(a.MaxAgeMS)*time.Millisecond), nil
}

func (r *forwardRunner) adopt(ctx context.Context, user string) (forward.Result, error) {
	uid, err := uuid.Parse(user)
	if err != nil || r.secrets == nil || r.accounts == nil || r.monitors == nil {
		return forward.Result{Status: 400, Class: "bad_request"}, nil
	}
	// The login nudge (design §3): the pod that took the login holds the session; the owner loads it from user_secrets and starts the
	// user's monitor, exactly as the boot hydrate does for a stored session. Idempotent; a user with no stored session is a no-op.
	sess, err := secrets.LoadRivianSession(ctx, r.secrets, uid)
	if err != nil {
		return forward.Result{}, err
	}
	if sess.UserSessionToken == "" {
		return forward.Result{Status: 404, Class: "not_connected", Body: json.RawMessage(`{}`)}, nil
	}
	r.accounts.For(uid).Restore(sess)
	r.monitors.Start(ctx, uid)
	r.logger.Info("forward: adopted a user's monitor on request", "user_id", uid.String())
	return forward.Result{Status: 200, Body: json.RawMessage(`{}`)}, nil
}

// forwardRuntime is what startForwardOwner hands back: the drain switch, and the requester side for the handlers (nil with the flag off,
// where every handler calls Rivian itself as before).
type forwardRuntime struct {
	setDraining func()
	forwarder   *forward.Forwarder
}

// startForwardOwner creates the forwarder's metrics (always, so the series exist at 0 on every pod) and, only when RIVOLT_FORWARD=1,
// runs the owner loop on its own Redis client. It returns the drain switch and the requester. With the flag off (the
// default) nothing runs and no key is read; with the flag on and no valid key the process refuses to start, so a pod never believes
// it forwards while it does not.
func startForwardOwner(ctx context.Context, logger *slog.Logger, appMetrics *metrics.Metrics, pool *sql.DB, store *leases.Store,
	accounts rivian.AccountRegistry, monitors *rivian.MonitorRegistry, secretsStore *secrets.Store, keyPrefix string) forwardRuntime {
	fm := forward.NewMetrics(appMetrics.Registry())
	if os.Getenv("RIVOLT_FORWARD") != "1" {
		return forwardRuntime{setDraining: func() {}}
	}
	addr := strings.TrimSpace(os.Getenv("RIVOLT_REDIS_ADDR"))
	key, kerr := forward.ParseKey(os.Getenv(forward.EnvKey))
	// Fail closed: a rolling deploy then stalls with the old pods serving, which is the safe failure. One clear line each, naming the
	// variable and never its value.
	if addr == "" {
		logger.Error("forward: RIVOLT_FORWARD=1 but RIVOLT_REDIS_ADDR is not set; refusing to start")
		os.Exit(1)
	}
	if kerr != nil {
		logger.Error("forward: RIVOLT_FORWARD=1 but " + forward.EnvKey + " is missing or not at least 32 random bytes in base64; refusing to start")
		os.Exit(1)
	}
	signer, err := forward.NewSigner(key)
	if err != nil {
		logger.Error("forward: signer", "err", err.Error())
		os.Exit(1)
	}
	ropts := forwardRedisOptions(addr, os.Getenv)
	logger.Info("forward: redis client", append([]any{"addr", addr, "pool", ropts.PoolSize}, redisAuthAttrs(ropts)...)...)
	rc := redis.NewClient(ropts)
	leaseView := forward.NewPGLeaseView(pool, store)
	owner, err := forward.NewOwner(forward.OwnerConfig{
		Pod:     store.PodID(),
		Keys:    forward.Keys{Prefix: keyPrefix},
		Signer:  signer,
		Redis:   rc,
		Leases:  leaseView,
		Runner:  &forwardRunner{store: store, secrets: secretsStore, accounts: accounts, monitors: monitors, logger: logger},
		Metrics: fm,
		Logger:  logger,
	})
	if err != nil {
		logger.Error("forward: owner", "err", err.Error())
		os.Exit(1)
	}
	fwd, err := forward.NewForwarder(forward.RequesterConfig{
		Pod:     store.PodID(),
		Keys:    forward.Keys{Prefix: keyPrefix},
		Signer:  signer,
		Redis:   rc,
		Leases:  leaseView,
		Metrics: fm,
		Logger:  logger,
	})
	if err != nil {
		logger.Error("forward: requester", "err", err.Error())
		os.Exit(1)
	}
	go owner.Run(ctx)
	logger.Info("forward: owner loop on", "pod", store.PodID())
	return forwardRuntime{setDraining: owner.SetDraining, forwarder: fwd}
}

// forwardReplayTest reports RIVOLT_FORWARD_REPLAY_TEST and refuses it anywhere but an install that can never reach Rivian: the switch makes
// the forwarder apply to the replay client as well as the live one, so the forwarding path can be driven on preview. It is an error unless
// RIVOLT_RIVIAN_LIVE_FORBIDDEN is true and the client is the replay client (a live client with the switch would put replay-grade
// shortcuts on a path that reaches Rivian). Unset or false: off, and nothing else is checked.
func forwardReplayTest(getenv func(string) string) (bool, error) {
	if !rivian.ParseBoolEnv(getenv("RIVOLT_FORWARD_REPLAY_TEST")) {
		return false, nil
	}
	if !rivian.ParseBoolEnv(getenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN")) {
		return false, errors.New("RIVOLT_FORWARD_REPLAY_TEST needs RIVOLT_RIVIAN_LIVE_FORBIDDEN=true")
	}
	if getenv("RIVIAN_CLIENT") != "replay" {
		return false, errors.New("RIVOLT_FORWARD_REPLAY_TEST needs RIVIAN_CLIENT=replay")
	}
	return true, nil
}
