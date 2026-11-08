package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// forwardRunner runs the forwarded kinds on this pod, the owner of the group. In M3-1 only adopt exists; the other kinds answer 501
// until their steps (fresh_state M3-2b, plan_legs M3-4, ota_notes M3-4b), and nothing sends them before then.
type forwardRunner struct {
	secrets  *secrets.Store
	accounts rivian.AccountRegistry
	monitors *rivian.MonitorRegistry
	logger   *slog.Logger
}

func (r *forwardRunner) Run(ctx context.Context, kind forward.Kind, user, _ string, _ any) (forward.Result, error) {
	if kind != forward.KindAdopt {
		return forward.Result{Status: 501, Class: "not_implemented"}, nil
	}
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

// startForwardOwner creates the forwarder's metrics (always, so the series exist at 0 on every pod) and, only when RIVOLT_FORWARD=1,
// runs the owner loop on its own Redis client. It returns the function to call when the pod starts draining. With the flag off (the
// default) nothing runs and no key is read; with the flag on and no valid key the process refuses to start, so a pod never believes
// it forwards while it does not.
func startForwardOwner(ctx context.Context, logger *slog.Logger, appMetrics *metrics.Metrics, pool *sql.DB, store *leases.Store,
	accounts rivian.AccountRegistry, monitors *rivian.MonitorRegistry, secretsStore *secrets.Store, keyPrefix string) (setDraining func()) {
	fm := forward.NewMetrics(appMetrics.Registry())
	setDraining = func() {}
	if os.Getenv("RIVOLT_FORWARD") != "1" {
		return setDraining
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
	owner, err := forward.NewOwner(forward.OwnerConfig{
		Pod:     store.PodID(),
		Keys:    forward.Keys{Prefix: keyPrefix},
		Signer:  signer,
		Redis:   redis.NewClient(ropts),
		Leases:  forward.NewPGLeaseView(pool, store),
		Runner:  &forwardRunner{secrets: secretsStore, accounts: accounts, monitors: monitors, logger: logger},
		Metrics: fm,
		Logger:  logger,
	})
	if err != nil {
		logger.Error("forward: owner", "err", err.Error())
		os.Exit(1)
	}
	go owner.Run(ctx)
	logger.Info("forward: owner loop on", "pod", store.PodID())
	return owner.SetDraining
}
