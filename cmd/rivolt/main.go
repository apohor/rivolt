// Command rivolt is the single-binary server for the Rivolt Rivian companion.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	// Embed the IANA time zone database so TZ=America/New_York etc. work
	// even on distroless images that don't ship /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/appsettings"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/idp"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/charges"
	rivoltcrypto "github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/elevation"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/invites"
	"github.com/apohor/rivolt/internal/signuprequests"
	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/ratelimit"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tracing"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/weather"
	"github.com/apohor/rivolt/internal/web"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/redis/go-redis/v9"
)

// version is stamped by the Docker build via -ldflags.
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h", "help":
			printUsage()
			return
		}
	}
	runServer()
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `rivolt — self-hosted Rivian companion

Usage:
  rivolt          Start the HTTP server (default)
  rivolt --help   Show this help

Environment:
  ADDR, DATA_DIR, VAPID_SUBJECT, OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY
  RIVIAN_CLIENT=stub|live|mock   (default: stub)
  RIVOLT_RESET_DATA=1            Wipe drives/charges/vehicle_state for the
                                 legacy "local" user on boot, then continue.
                                 Vehicles/settings/push are preserved.
`)
}

func runServer() {
	// Build the slog handler:
	//   RIVOLT_LOG_LEVEL = debug|info|warn|error  (default: info)
	//   RIVOLT_LOG_FORMAT = json|text             (default: json)
	// ContextHandler wraps whatever inner handler we pick so every
	// log line emitted while serving a request automatically gets
	// request_id / user_id / vehicle_id / trace_id from context —
	// no callsite changes needed in internal/* packages.
	level := parseLogLevel(os.Getenv("RIVOLT_LOG_LEVEL"))
	var inner slog.Handler
	switch strings.ToLower(os.Getenv("RIVOLT_LOG_FORMAT")) {
	case "text":
		inner = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	default:
		inner = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	logger := slog.New(logging.NewContextHandler(inner))
	slog.SetDefault(logger)

	addr := flag.String("addr", envOr("ADDR", ":8080"), "HTTP listen address")
	dataDir := flag.String("data-dir", envOr("DATA_DIR", "./data"), "directory for the SQLite database and caches")
	vapidSubject := flag.String("vapid-subject", envOr("VAPID_SUBJECT", "mailto:rivolt@invalid"),
		"VAPID JWT subject. Must be a real mailto: or https: URL for iPhone — Apple's push service rejects @example addresses.")
	vapidPub := flag.String("vapid-public-key", os.Getenv("VAPID_PUBLIC_KEY"), "VAPID public key (optional; generated on first run if unset)")
	vapidPriv := flag.String("vapid-private-key", os.Getenv("VAPID_PRIVATE_KEY"), "VAPID private key (optional; generated on first run if unset)")
	openAIKey := flag.String("openai-api-key", os.Getenv("OPENAI_API_KEY"), "OpenAI API key (or OPENAI_API_KEY env)")
	anthropicKey := flag.String("anthropic-api-key", os.Getenv("ANTHROPIC_API_KEY"), "Anthropic API key (or ANTHROPIC_API_KEY env)")
	geminiKey := flag.String("gemini-api-key", firstNonEmpty(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY")), "Google Gemini API key (or GEMINI_API_KEY / GOOGLE_API_KEY env)")
	flag.Parse()
	// AI provider keys are install-wide ("operator pays the bill"
	// for all users on this rivolt). They are persisted in the
	// app_settings table (envelope-encrypted via crypto.Sealer)
	// and managed through /api/admin/settings/ai. The CLI flags /
	// env vars remain accepted as a one-time seed for first boot
	// — once a value is in app_settings, the stored row wins,
	// so rotating a key from the admin UI takes effect even if
	// the helm chart still has the old one in env. See
	// internal/settings.NewManager + internal/appsettings.

	logger.Info("rivolt starting",
		"version", version,
		"addr", *addr,
		"data_dir", *dataDir,
		"tz", time.Now().Location().String(),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Bring up OTel before anything that might want to record a
	// span (DB pings, Rivian token refresh on boot). Init returns
	// a no-op shutdown when RIVOLT_OTEL_ENABLED is unset, so the
	// docker-compose / single-binary path stays untouched.
	tracingShutdown, err := tracing.Init(ctx, version)
	if err != nil {
		logger.Error("tracing init failed", "err", err.Error())
		os.Exit(1)
	}
	defer func() {
		// Bounded shutdown — Tempo or its OTLP receiver being
		// unreachable should not stall pod termination.
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracingShutdown(sctx); err != nil {
			logger.Warn("tracing shutdown error", "err", err.Error())
		}
	}()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		logger.Error("cannot create data dir", "path", *dataDir, "err", err.Error())
		os.Exit(1)
	}

	// Postgres is the only backend as of v0.4.2. The data dir still
	// holds auto-generated secrets (cookie_secret, VAPID keys) so
	// the volume mount stays.
	var pgPool *sql.DB
	{
		dsn := postgresDSN()
		if dsn == "" {
			logger.Error("DATABASE_URL (or DB_HOST/DB_USER/DB_PASSWORD/DB_NAME) is required")
			os.Exit(1)
		}
		// 5 minutes covers the worst-case migration we ship
		// (0007's partition swap on an already-populated
		// vehicle_state heap). Ping itself is sub-second; only
		// migrations can legitimately take this long, and they
		// run exactly once per upgrade.
		pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		p, err := db.Open(pctx, dsn)
		cancel()
		if err != nil {
			logger.Error("postgres open failed", "err", err.Error())
			os.Exit(1)
		}
		pgPool = p
		logger.Info("postgres connected")
	}

	// Per-user data-plane factories. Each handler/recorder
	// resolves f.For(uid) on demand so writes always land under
	// the correct user_id. Factories are cheap; *Store
	// instances are cached internally.
	resolverFactory := db.NewVehicleResolverFactory(pgPool)
	drivesFactory := drives.NewFactory(pgPool, resolverFactory)
	chargesFactory := charges.NewFactory(pgPool, resolverFactory)
	samplesFactory := samples.NewFactory(pgPool, resolverFactory)
	settingsFactory := settings.NewFactory(pgPool)
	pushFactory := push.NewFactory(pgPool)
	tripsFactory := trips.NewFactory(pgPool)

	// Envelope-encrypted secret store. Backs the rivian.Session
	// blob (previously plaintext in settings_kv) and, later, AI
	// keys + per-user VAPID private keys.
	//
	// RIVOLT_KEK is required in production (32-byte AES-256 key,
	// base64-encoded, prefixed with "<kekID>:"). Absence is
	// tolerated only when RIVOLT_ALLOW_NOOP_SEALER=1 — strictly
	// for the local dev workstation; the env-var name is
	// deliberately long and ugly so it never ends up in a helm
	// chart or compose file.
	var secretsStore *secrets.Store
	var sealer rivoltcrypto.Sealer
	if pgPool != nil {
		s, serr := buildSealer(logger)
		if serr != nil {
			logger.Error("sealer setup failed — refusing to start", "err", serr.Error())
			os.Exit(1)
		}
		sealer = s
		secretsStore = secrets.New(pgPool, sealer)
		logger.Info("secret store ready", "kek_id", sealer.KEKID())
	}

	// Operational flag store backs the Rivian upstream kill
	// switch (ARCHITECTURE decision 6). Non-critical: when open
	// fails we log and carry on — the hot path treats a nil
	// store as "gate always open", matching the legacy
	// pre-kill-switch behavior.
	flagsStore, err := flags.OpenStore(ctx, pgPool, logger)
	if err != nil {
		logger.Warn("flags store unavailable", "err", err.Error())
	}
	if flagsStore != nil {
		flagsStore.Start(ctx)
	}

	// VAPID is server-wide (push_vapid is a single-row table).
	// Subscriptions are per-user via pushFactory; the Service
	// only needs the keypair to sign outbound notifications, so
	// it's wired against a server-scoped store that can read/write
	// VAPID without binding to a user. Per-user fan-out
	// (digest sender) constructs its own service per request.
	var pushSvc *push.Service
	if pgPool != nil {
		serverPushStore := push.NewServerStore(pgPool)
		vapid, verr := push.LoadOrGenerateVAPID(ctx, serverPushStore, *vapidPub, *vapidPriv, *vapidSubject)
		if verr != nil {
			logger.Warn("VAPID setup failed", "err", verr.Error())
		} else {
			pushSvc = push.NewService(serverPushStore, vapid, logger)
		}
	}

	var rivianClient rivian.Client
	var accountRegistry rivian.AccountRegistry
	// monitorRegistry holds one *StateMonitor per user with a live
	// session, each wired to that user's per-user data-plane stores.
	// Built unconditionally; live mode populates it from the boot
	// hydrate sweep below, and Login/Logout drive the runtime
	// lifecycle. Nil in stub mode.
	var monitorRegistry *rivian.MonitorRegistry
	// sharedRedis is the Redis client (when configured) used by the
	// rate limiter and the live-session persistence layer. Hoisted to
	// the function scope so the post-switch monitorRegistry wiring
	// can install a LiveStateStore factory against the same client.
	var sharedRedis *redis.Client
	// settingsMgr is constructed further below (after sealer + auth
	// setup) but the recorder's drive-close hook factory needs to
	// reference it before the boot hydrate sweep starts new monitors.
	// Declared here so the closure captures the variable; the late
	// assignment populates it before any drive actually closes.
	var settingsMgr *settings.Manager
	// appMetrics owns the Prometheus registry. Built before the
	// rivian client so the breaker observer (which writes to the
	// breaker gauge/counter) and the lease coordinator (which
	// writes to the leases gauge) can both wire in at construction.
	appMetrics := metrics.New()
	// Install-wide Redis key prefix. Defaults to "rivolt"; the preview
	// environment sets "rivolt-preview" so preview and prod can share
	// one Redis without colliding on the rate-limiter token buckets
	// (per-user livestate keys are already disjoint by user_id, but
	// the limiter classes are install-wide constants). Hoisted out
	// of the rivian-client switch so the livestate factory below
	// can read the same value.
	redisKeyPrefix := strings.TrimSpace(os.Getenv("RIVOLT_REDIS_KEY_PREFIX"))
	if redisKeyPrefix == "" {
		redisKeyPrefix = "rivolt"
	}
	switch clientMode := os.Getenv("RIVIAN_CLIENT"); clientMode {
	case "mock":
		// Mock starts logged-out; the UI sign-in panel drives Login()
		// just like the live client. Per-user instances so two test
		// users in the same dev session don't share authentication
		// state.
		accountRegistry = rivian.NewMockAccountRegistry(func(_ uuid.UUID) *rivian.MockClient {
			return rivian.NewMock()
		})
		// Stub-shaped fallback for the global rivian.Client field.
		// Live read paths (Vehicles/State) for mock users are
		// resolved per-request via clientFor(d, uid) → registry.
		rivianClient = rivian.NewStub()
		if secretsStore == nil {
			logger.Info("rivian client: mock (no secrets store; login state will not persist)")
		} else {
			logger.Info("rivian client: mock (awaiting login)")
		}
	case "stub":
		rivianClient = rivian.NewStub()
		accountRegistry = rivian.NewNopAccountRegistry()
		logger.Info("rivian client: stub (no network)")
	default:
		// Live is the default. Auth happens later via Settings; the
		// server comes up fine without credentials, and Vehicles/State
		// just return a 'not authenticated' error until the user logs
		// in.
		//
		// Per-user *LiveClient via AccountRegistry: each For(uid)
		// constructs a fresh client wired with the same shared
		// breaker / rate-limit / kill-switch / version stamp the
		// pre-multi-user singleton received. Reauth-sink persistence
		// is keyed by the user the closure was built for, so two
		// concurrent users' needs_reauth flags can't clobber each
		// other.
		breaker := rivian.NewBreaker(rivian.DefaultBreakerConfig(), &breakerMetrics{m: appMetrics})
		var sharedLimiter *ratelimit.Limiter
		if addr := strings.TrimSpace(os.Getenv("RIVOLT_REDIS_ADDR")); addr != "" {
			rdb := redis.NewClient(&redis.Options{Addr: addr})
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			rlCfg := ratelimit.DefaultConfig()
			rlCfg.KeyPrefix = redisKeyPrefix + ":rl"
			limiter, err := ratelimit.New(pingCtx, rdb, rlCfg, logger)
			pingCancel()
			if err != nil {
				logger.Warn("ratelimit: disabled (redis unreachable)", "addr", addr, "err", err.Error())
				_ = rdb.Close()
			} else {
				sharedLimiter = limiter
				sharedRedis = rdb
				logger.Info("ratelimit: enabled", "addr", addr, "key_prefix", rlCfg.KeyPrefix)
			}
		}

		buildLive := func(uid uuid.UUID) *rivian.LiveClient {
			lc := rivian.NewLive().WithRivoltVersion(version)
			if flagsStore != nil {
				// Gate every outbound Rivian call on the kill switch.
				// Cheap atomic load; returns ErrUpstreamPaused when
				// the operator has flipped the flag.
				lc.WithUpstreamGate(func(_ context.Context) error {
					if ks := flagsStore.KillSwitch(); ks.Paused {
						return rivian.ErrUpstreamPaused
					}
					return nil
				})
			}
			lc.WithBreaker(breaker)
			if sharedLimiter != nil {
				lc.WithRateLimit(&rateLimitMetrics{l: sharedLimiter, m: appMetrics})
			}
			// Persist needs_reauth transitions to Postgres so the
			// flag survives restarts. Closure captures uid so each
			// per-user client persists to its own row.
			boundUID := uid
			lc.WithReauthSink(func(sinkCtx context.Context, reason string) {
				if err := db.SetNeedsReauth(sinkCtx, pgPool, boundUID, reason); err != nil {
					logger.Warn("persist needs_reauth", "user_id", boundUID.String(), "reason", reason, "err", err.Error())
				}
			})
			// Prime the in-memory mirror from Postgres so a crash
			// loop with stale creds doesn't briefly allow requests
			// until the first classification lands.
			if needs, reason, err := db.GetNeedsReauth(ctx, pgPool, boundUID); err != nil {
				logger.Warn("load needs_reauth", "user_id", boundUID.String(), "err", err.Error())
			} else if needs {
				lc.SetNeedsReauth(true, reason)
				logger.Info("rivian client: needs re-auth (from Postgres)",
					"user_id", boundUID.String(), "reason", reason)
			}
			return lc
		}
		accountRegistry = rivian.NewLiveAccountRegistry(buildLive)
		// Stub fallback for the unauthenticated stub-of-last-resort
		// path. Per-user resolution at the handler layer always
		// picks up the registry first (see clientFor in api).
		rivianClient = rivian.NewStub()
	}

	// Build the monitor registry. In live/mock modes the registry
	// is the only thing that owns *StateMonitor instances. Stub
	// mode skips it (no recorder, no WS).
	if accountRegistry != nil && os.Getenv("RIVIAN_CLIENT") != "stub" {
		monitorRegistry = rivian.NewMonitorRegistry(
			pgPool, accountRegistry,
			drivesFactory, chargesFactory, samplesFactory, settingsFactory,
			logger,
		)
		monitorRegistry.SetParent(ctx)
		// Live-session persistence: when Redis is wired, install a
		// per-user LiveStateStore factory so each StateMonitor can
		// rehydrate its in-flight drive/charge accumulators across
		// pod restarts and lease handoffs. Without this, a deploy or
		// a peer pod taking over a lease mid-drive fragments the
		// trip into a fresh drive row at the handover (see incident
		// on 2026-05-07).
		if sharedRedis != nil {
			monitorRegistry.SetLiveStateStoreFactory(func(uid uuid.UUID) rivian.LiveStateStore {
				return rivian.NewRedisLiveStateStore(sharedRedis, uid.String(), redisKeyPrefix)
			})
			logger.Info("live state store: enabled (redis)")
		}
		// Drive-close enrichment: when a drive closes (D→P), spawn a
		// goroutine that fetches Open-Meteo weather for the start
		// hour + the per-cadence series, gated on the operator's
		// recap.weather_enabled toggle. Without this, weather only
		// landed when the user clicked "Backfill" in the SPA, so
		// recently recorded drives showed up without weather data.
		if pgPool != nil {
			weatherCache := weather.NewCache(pgPool)
			monitorRegistry.SetDriveCloseHookFactory(func(uid uuid.UUID) rivian.DriveCloseHook {
				return func(ctx context.Context, drv drives.Drive) {
					if settingsMgr == nil || !settingsMgr.RecapWeatherEnabled() {
						return
					}
					if _, err := weather.FetchAndCache(
						ctx, weatherCache, uid, drv.ID,
						drv.StartedAt, drv.EndedAt,
						drv.StartLat, drv.StartLon,
						drv.EndLat, drv.EndLon,
					); err != nil {
						logger.Debug("auto weather fetch failed",
							"user_id", uid.String(),
							"drive_id", drv.ID,
							"err", err.Error())
					}
				}
			})
			logger.Info("drive close hook: weather auto-fetch enabled")
		}
		// ChargeCloseHook: deliver per-user "charging complete" push
		// notifications. Gated on pushSvc being constructed (a missing
		// VAPID keypair leaves pushSvc nil, in which case the factory
		// returns nil and the hook is never installed).
		if pushSvc != nil && pushFactory != nil {
			monitorRegistry.SetChargeCloseHookFactory(func(uid uuid.UUID) rivian.ChargeCloseHook {
				userStore := pushFactory.For(uid)
				if userStore == nil {
					return nil
				}
				return func(_ context.Context, c charges.Charge) {
					// Skip rows the recorder considers no-ops (very
					// short / zero-delta charges). The recorder's
					// phantom guard already filters before the hook
					// fires, but checking here too costs nothing.
					if !c.EndedAt.After(c.StartedAt) {
						return
					}
					summary := fmt.Sprintf(
						"Ended at %d%% · %.1f kWh added",
						int(c.EndSoCPct), c.EnergyAddedKWh,
					)
					pushSvc.NotifyChargingDone(userStore, c.ID, summary)
				}
			})
			logger.Info("charge close hook: charging-done notifications enabled")
		}
		// Elevation lookup is opt-in: a self-hosted instance never
		// phones an off-LAN tile server unless the operator says so.
		// ELEVATION_ENABLED=1 turns it on; ELEVATION_TILES_URL points
		// at a self-hosted Terrarium mirror (defaults to Mapzen's AWS
		// Open Data endpoint, which is convenient but leaks per-tile
		// coordinates off-LAN — see ROADMAP "Self-hosted elevation").
		// ELEVATION_CACHE_DIR persists fetched PNGs to disk so a
		// pod restart doesn't re-fetch every tile, and so the operator
		// can rsync a pre-built Terrarium dump there for fully-offline
		// runs (point ELEVATION_TILES_URL at a blackholed/in-cluster
		// upstream and let the disk cache do the work).
		// Recorder writes NULL altitude_m when disabled or on cache
		// misses; the frontend hides the chart when no samples carry it.
		if os.Getenv("ELEVATION_ENABLED") == "1" {
			tileURL := os.Getenv("ELEVATION_TILES_URL")
			cacheDir := os.Getenv("ELEVATION_CACHE_DIR")
			source := "self-hosted"
			if tileURL == "" {
				source = "mapzen-terrarium (off-LAN)"
			}
			elevResolver := elevation.New(elevation.Config{
				TileURL:  tileURL,
				CacheDir: cacheDir,
				Logger:   logger.With("component", "elevation"),
			})
			monitorRegistry.SetElevationLookup(elevResolver)
			logger.Info("elevation lookup enabled",
				"source", source,
				"cache_dir", cacheDir,
			)
		} else {
			logger.Info("elevation lookup disabled", "reason", "ELEVATION_ENABLED!=1 (opt-in)")
		}
	}

	// Boot-time multi-user hydrate sweep (live mode only). Every
	// user with a persisted rivian.session blob in user_secrets
	// gets their own *LiveClient pre-built and Restore()'d, then
	// a StateMonitor started for them — before the HTTP server
	// accepts traffic. A pod restart is therefore invisible to
	// the data plane: AuthReady fires during boot, the WS
	// subscriber proceeds straight into Subscribe, no UI traffic
	// is required to "wake up" recording.
	//
	// Pre-warm to avoid the missed-drive class of bug: a shared
	// LiveClient that lazy-hydrates via rivianHydrateMW silently
	// disables telemetry across pod restarts until someone opens
	// the SPA. Eagerly subscribing here keeps the data plane
	// running regardless of UI traffic.
	if os.Getenv("RIVIAN_CLIENT") != "stub" && os.Getenv("RIVIAN_CLIENT") != "mock" {
		if secretsStore == nil {
			logger.Info("rivian client: live (no secrets store; login state will not persist)")
		} else {
			uids, err := db.ListUsersWithRivianSession(ctx, pgPool)
			if err != nil {
				logger.Warn("rivian boot hydrate: list users failed", "err", err.Error())
			}
			restored := 0
			for _, uid := range uids {
				sess, err := secrets.LoadRivianSession(ctx, secretsStore, uid)
				if err != nil {
					logger.Warn("rivian boot hydrate failed",
						"user_id", uid.String(), "err", err.Error())
					continue
				}
				if sess.UserSessionToken == "" {
					continue
				}
				userLC := accountRegistry.For(uid).(*rivian.LiveClient)
				userLC.Restore(sess)
				if monitorRegistry != nil {
					monitorRegistry.Start(ctx, uid)
				}
				restored++
				logger.Info("rivian session restored",
					"user_id", uid.String(), "email", sess.Email)
			}
			logger.Info("rivian client: live", "users_hydrated", restored)
		}
	}

	// leaseCoordinator runs the multi-replica subscription
	// reconciliation loop. nil when pgPool is nil (single-binary
	// path) — the eager-subscribe fallback covers that case.
	var leaseCoordinator *leases.Coordinator

	// Keep `vehicle_state` monthly partitions rolling. Without
	// this a pod that runs past the last partition created at
	// migration time would start rejecting live-recorder writes
	// with "no partition of relation … found for row". Fire-
	// and-forget goroutine; ctx cancellation stops it on
	// SIGTERM.
	if pgPool != nil {
		partitionJanitor := samples.NewPartitionJanitor(pgPool)
		go partitionJanitor.Run(ctx)
	}

	// Subscription leases gate which vehicles THIS pod owns.
	// Multi-replica steady state requires exactly one pod per
	// vehicle; the leases.Coordinator polls Postgres every 30s,
	// claims unowned vehicles, drops ones it loses, and fires
	// EnsureSubscribed/Unsubscribe to keep the WS subscription
	// set in sync. The monitor registry routes by
	// rivian_vehicle_id → owner uid → that user's monitor.
	//
	// pod_id source: RIVOLT_POD_ID env (set via the k8s downward
	// API in the Helm chart). Falls back to hostname so the
	// docker-compose path works without extra config.
	if monitorRegistry != nil && pgPool != nil {
		podID := os.Getenv("RIVOLT_POD_ID")
		if podID == "" {
			if h, err := os.Hostname(); err == nil && h != "" {
				podID = h
			} else {
				logger.Error("cannot derive pod id; set RIVOLT_POD_ID")
				os.Exit(1)
			}
		}
		leaseStore, err := leases.NewStore(pgPool, podID)
		if err != nil {
			logger.Error("lease store", "err", err.Error())
			os.Exit(1)
		}
		vehicleSource := leases.NewVehicleSource(
			func() []string {
				infos := monitorRegistry.AllVehicleInfo()
				out := make([]string, 0, len(infos))
				for _, v := range infos {
					if v.ID != "" {
						out = append(out, v.ID)
					}
				}
				return out
			},
			logger,
			func(qctx context.Context) ([]string, error) {
				// Skip the legacy electrafi-<hash> synthetic vehicle
				// rows left over from earlier importers. They aren't
				// real Rivian VINs, so leasing them only burns
				// subscription slots on dead WS streams.
				return leases.QueryStringColumn(qctx, pgPool,
					`SELECT DISTINCT rivian_vehicle_id FROM vehicles
					   WHERE rivian_vehicle_id <> ''
					     AND rivian_vehicle_id NOT LIKE 'electrafi-%'`)
			},
			func(qctx context.Context) ([]string, error) {
				return leases.QueryStringColumn(qctx, pgPool,
					`SELECT DISTINCT vehicle_id FROM subscription_leases
					   WHERE vehicle_id <> ''
					     AND vehicle_id NOT LIKE 'electrafi-%'`)
			},
		)
		coord := leases.NewCoordinator(
			leaseStore,
			vehicleSource,
			monitorRegistry.EnsureSubscribed,
			monitorRegistry.Unsubscribe,
			logger,
		)
		coord.SetCountObserver(func(n int) {
			appMetrics.SubscriptionLeases.Set(float64(n))
		})
		go func() {
			if err := coord.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("lease coordinator exited", "err", err.Error())
			}
		}()
		// Prime per-vehicle metadata across every running monitor,
		// then poke the coordinator so it reconciles immediately
		// once AllVehicleInfo() is populated.
		go func() {
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			// Refresh runs per-monitor inside RefreshAll; we just
			// trigger it through each loaded user.
			for _, uid := range accountRegistry.Loaded() {
				m := monitorRegistry.For(uid)
				if m == nil {
					continue
				}
				if err := m.RefreshVehicleInfo(rctx); err != nil {
					logger.Warn("vehicle info refresh failed",
						"user_id", uid.String(), "err", err.Error())
				}
			}
			coord.TriggerReconcile()
		}()
		leaseCoordinator = coord
	}

	webFS := web.Assets()
	if webFS == nil {
		logger.Warn("embedded web bundle missing; SPA routes will 404 until `make web` is run")
	}

	// Auth wiring. Rivolt has three issuers: OIDC sign-in (the
	// default for any real deployment), a trusted-upstream-proxy
	// header (oauth2-proxy / Authelia in front), and a debug
	// bypass that hard-injects a user without any credential
	// check. With none of them configured the API stays open —
	// the legacy single-tenant docker-compose UX.
	//
	// RIVOLT_COOKIE_SECRET should be a hex string of at least 64 chars
	// (32 bytes). If empty, a random key is generated on boot and
	// every restart invalidates all sessions — fine for first-run,
	// wrong for anyone who doesn't like being logged out twice a
	// week.
	//
	// RIVOLT_TRUSTED_PROXY_CIDR enables Option-B SSO: comma-separated
	// subnets whose X-Forwarded-Preferred-Username header will be
	// honoured. Empty (the default) means header-based auth is off,
	// and a forged header from any client is ignored.
	//
	// RIVOLT_SECURE_COOKIE defaults to true; set to "false" for pure
	// http:// deployments where the browser would otherwise refuse
	// to store the session cookie.
	//
	// RIVOLT_AUTH_BYPASS_USER, when set, makes every unauthenticated
	// request resolve to the named user. Local-dev only — it's the
	// equivalent of disabling auth, gated by an explicit opt-in env
	// so production never lights it up by accident.
	trustedNets, err := auth.ParseTrustedCIDRs(os.Getenv("RIVOLT_TRUSTED_PROXY_CIDR"))
	if err != nil {
		logger.Error("bad RIVOLT_TRUSTED_PROXY_CIDR", "err", err.Error())
		os.Exit(1)
	}
	cookieSecret, err := decodeHexSecret(os.Getenv("RIVOLT_COOKIE_SECRET"))
	if err != nil {
		logger.Error("bad RIVOLT_COOKIE_SECRET", "err", err.Error())
		os.Exit(1)
	}
	// When the operator hasn't pinned a secret via env, persist a
	// generated one under DATA_DIR so sessions survive restarts.
	// The file lives inside the same volume the operator is
	// already backing up; anyone who can read it also has the full
	// SQLite database, so the threat model is unchanged. Rotating
	// the secret is `rm ${DATA_DIR}/cookie_secret` on the host.
	var cookieSecretSource string
	switch {
	case len(cookieSecret) > 0:
		cookieSecretSource = "env"
	default:
		secretPath := filepath.Join(*dataDir, "cookie_secret")
		cookieSecret, err = loadOrCreateCookieSecret(secretPath)
		if err != nil {
			logger.Error("cookie secret", "path", secretPath, "err", err.Error())
			os.Exit(1)
		}
		cookieSecretSource = "file:" + secretPath
	}
	secureCookie := true
	if v := os.Getenv("RIVOLT_SECURE_COOKIE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			logger.Error("bad RIVOLT_SECURE_COOKIE", "value", v, "err", err.Error())
			os.Exit(1)
		}
		secureCookie = b
	}

	// Debug bypass: when RIVOLT_AUTH_BYPASS_USER is set, every
	// unauthenticated request resolves to that user. We refuse to
	// enable it when SecureCookie is true (i.e. probably-prod) so
	// a typo in env config can't silently turn off auth on the
	// public internet.
	var bypassUserID uuid.UUID
	if bypassUser := strings.TrimSpace(os.Getenv("RIVOLT_AUTH_BYPASS_USER")); bypassUser != "" {
		if secureCookie {
			logger.Error("RIVOLT_AUTH_BYPASS_USER refused while RIVOLT_SECURE_COOKIE!=false; this is a debug-only knob")
			os.Exit(1)
		}
		if pgPool != nil {
			if _, err := db.EnsureUser(ctx, pgPool, bypassUser); err != nil {
				logger.Error("bypass user ensure", "username", bypassUser, "err", err.Error())
				os.Exit(1)
			}
		}
		bypassUserID = db.UserIDFor(bypassUser)
		logger.Warn("AUTH BYPASS ENABLED — every request resolves to this user. DO NOT USE IN PRODUCTION.",
			"username", bypassUser,
			"user_id", bypassUserID.String(),
		)
	}

	// Kratos client — initialised early so the auth Service's
	// KratosResolver closure can capture it. Disabled when
	// KRATOS_ADMIN_URL is unset; the resolver then becomes a
	// no-op and only the cookie / header / OIDC issuers run.
	kratosClient, err := kratos.NewFromEnv()
	if err != nil {
		logger.Error("kratos init", "err", err.Error())
		os.Exit(1)
	}

	authSvc, err := auth.New(auth.Config{
		CookieSecret:      cookieSecret,
		SecureCookie:      secureCookie,
		TrustedProxyCIDRs: trustedNets,
		UserIDFor:         db.UserIDFor,
		UsernameFor: func(ctx context.Context, uid uuid.UUID) (string, error) {
			if pgPool == nil {
				return "", nil
			}
			return db.LookupUsername(ctx, pgPool, uid)
		},
		RoleFor: func(ctx context.Context, uid uuid.UUID) (string, error) {
			if pgPool == nil {
				return "", nil
			}
			return db.RoleFor(ctx, pgPool, uid)
		},
		DisabledFor: func(ctx context.Context, uid uuid.UUID) (bool, error) {
			if pgPool == nil {
				return false, nil
			}
			return db.IsDisabled(ctx, pgPool, uid)
		},
		BypassUserID: bypassUserID,
		// Kratos session issuer — only invoked when the inbound
		// request actually carries the ory_kratos_session cookie,
		// so the per-request Whoami round-trip is paid only by
		// Hydra/Kratos-authenticated users. Resolver returns
		// (uid, true, nil) on hit, (zero, false, nil) on no
		// session, (zero, false, err) on transient infra error
		// (which middleware treats as no session).
		KratosResolver: func(ctx context.Context, cookieHeader string) (uuid.UUID, bool, error) {
			if kratosClient == nil || !kratosClient.Enabled() {
				return uuid.Nil, false, nil
			}
			id, err := kratosClient.Whoami(ctx, cookieHeader)
			if err != nil {
				if errors.Is(err, kratos.ErrNoSession) {
					return uuid.Nil, false, nil
				}
				return uuid.Nil, false, err
			}
			email := strings.ToLower(strings.TrimSpace(id.Traits.Email))
			if email == "" {
				return uuid.Nil, false, nil
			}
			// EnsureUserFull guarantees a row exists with the
			// same UUIDv5(email) the cookie / OIDC paths use,
			// so a user who first appeared via Kratos resolves
			// to the same identity downstream.
			if pgPool != nil {
				uid, err := db.EnsureUserFull(ctx, pgPool, email, email, id.Traits.DisplayName)
				if err != nil {
					return uuid.Nil, false, err
				}
				return uid, true, nil
			}
			return db.UserIDFor(email), true, nil
		},
	})
	if err != nil {
		logger.Error("auth init", "err", err.Error())
		os.Exit(1)
	}
	// Wire the opaque-session store when Postgres is present.
	// Without a DB we fall back to auth's legacy HMAC-signed
	// cookie path so the in-memory / no-DB dev mode still
	// boots. The same cookieSecret doubles as the sessions
	// pepper — in production both sinks need the value to be
	// stable (rotating either invalidates cookies, which is
	// fine on a credentials bump but catastrophic otherwise).
	if pgPool != nil {
		sStore, serr := sessions.New(pgPool, cookieSecret)
		if serr != nil {
			logger.Error("sessions init", "err", serr.Error())
			os.Exit(1)
		}
		authSvc.WithSessionStore(sessions.NewAuthAdapter(sStore))
		logger.Info("sessions store ready — cookies are opaque, revocable")
	}

	// OIDC: third issuer alongside static creds + trusted-proxy
	// header. Disabled when RIVOLT_OIDC_PROVIDERS is empty so a
	// fresh install ships zero behaviour change. When enabled it
	// requires pgPool (we need EnsureUserFull and a sessions
	// store) — emit a clear error rather than silently dropping.
	var oidcSvc *oidc.Service
	if provs, perr := oidc.ParseProvidersFromEnv(os.Getenv, os.Getenv("RIVOLT_BASE_URL")); perr != nil {
		logger.Error("oidc env parse", "err", perr.Error())
		os.Exit(1)
	} else if len(provs) > 0 {
		if pgPool == nil {
			logger.Error("oidc requires DATABASE_URL (sessions + users tables)")
			os.Exit(1)
		}
		svc, oerr := oidc.New(ctx, oidc.Config{
			IssueSession: authSvc.IssueSession,
			EnsureUser: func(ctx context.Context, username, email, displayName string) (uuid.UUID, error) {
				uid, err := db.EnsureUserFull(ctx, pgPool, username, email, displayName)
				if err != nil {
					return uuid.Nil, err
				}
				// Refuse to mint a session for an admin-
				// disabled row. The Middleware path catches
				// this on every subsequent request too, but
				// blocking here keeps the UX honest: the user
				// gets an immediate 403 from the IdP
				// callback, not a redirect-loop into /login.
				disabled, derr := db.IsDisabled(ctx, pgPool, uid)
				if derr != nil {
					return uuid.Nil, derr
				}
				if disabled {
					return uuid.Nil, oidc.ErrUserForbidden
				}
				return uid, nil
			},
			UserIDFor:    db.UserIDFor,
			PostLoginURL: "/",
			SecureCookie: secureCookie,
			Logger:       logger,
			Providers:    provs,
		})
		if oerr != nil {
			logger.Error("oidc init", "err", oerr.Error())
			os.Exit(1)
		}
		oidcSvc = svc
		names := make([]string, 0, len(provs))
		for _, p := range provs {
			names = append(names, p.Name)
		}
		logger.Info("oidc enabled", "providers", names)
	}

	authEnforced := oidcSvc != nil || len(trustedNets) > 0 || bypassUserID != uuid.Nil
	if authEnforced {
		logger.Info("auth enforced",
			"oidc", oidcSvc != nil,
			"trusted_cidrs", len(trustedNets),
			"bypass", bypassUserID != uuid.Nil,
			"secure_cookie", secureCookie,
			"cookie_secret", cookieSecretSource,
		)
	} else {
		logger.Warn("auth not enforced — API is open. Configure RIVOLT_OIDC_PROVIDERS, RIVOLT_TRUSTED_PROXY_CIDR, or RIVOLT_AUTH_BYPASS_USER to enable.")
	}

	// Install-wide settings manager (AI provider keys + models).
	// Backed by the app_settings table, sealed with the same KEK
	// the rest of the secrets pipeline uses. Env-seeded on first
	// boot so a freshly deployed install with OPENAI_API_KEY in
	// helm values comes up already configured; once a row is in
	// app_settings, the stored value wins (the admin can rotate
	// from the UI without re-deploying). Variable was declared
	// near the registry hoists above so the recorder's drive-close
	// hook factory can capture it before the boot hydrate sweep.
	if pgPool != nil && sealer != nil {
		appKV, err := appsettings.New(pgPool, sealer)
		if err != nil {
			logger.Error("appsettings init", "err", err.Error())
			os.Exit(1)
		}
		mgr, err := settings.NewManager(ctx, appKV, settings.AIConfig{
			OpenAIKey:    *openAIKey,
			AnthropicKey: *anthropicKey,
			GeminiKey:    *geminiKey,
		})
		if err != nil {
			logger.Error("settings manager init", "err", err.Error())
			os.Exit(1)
		}
		settingsMgr = mgr
	}

	// Identity provider — provisioning of users on
	// POST /api/admin/users and POST /api/signup.
	//
	// Kratos is the only backend; when KRATOS_ADMIN_URL is unset the
	// provider is disabled (every mutating call returns an error) and
	// the rivolt admin endpoint only creates the rivolt DB row.
	//
	// Note: kratosClient is initialised earlier (before auth.New)
	// so the auth Service's KratosResolver closure can capture it.
	// Hydra admin client — drives the custom login + consent UI
	// mounted at /api/auth/hydra. Disabled when HYDRA_ADMIN_URL is
	// unset; without it the OIDC bridge is absent and Rivolt only
	// serves its own SPA without a federated sign-in path.
	hydraClient, err := hydra.NewFromEnv()
	if err != nil {
		logger.Error("hydra init", "err", err.Error())
		os.Exit(1)
	}
	if hydraClient.Enabled() && kratosClient.Enabled() {
		logger.Info("hydra OIDC bridge enabled",
			"admin", os.Getenv("HYDRA_ADMIN_URL"),
			"kratos_public", os.Getenv("KRATOS_PUBLIC_URL"))
	} else if hydraClient.Enabled() {
		logger.Warn("hydra is configured but kratos public URL is missing — /auth/hydra/* will be disabled",
			"hint", "set KRATOS_PUBLIC_URL alongside KRATOS_ADMIN_URL")
	}
	// How long Hydra should remember a user's login + consent
	// before re-prompting. Drives the RememberFor field on
	// AcceptLoginRequest / AcceptConsentRequest. Zero defers to
	// Hydra's own login_session lifespan; a positive duration
	// caps the silent-refresh window from the RP's perspective.
	hydraRememberFor := 24 * time.Hour
	if v := strings.TrimSpace(os.Getenv("RIVOLT_OIDC_HYDRA_REMEMBER_FOR")); v != "" {
		parsed, perr := time.ParseDuration(v)
		if perr != nil {
			logger.Error("invalid RIVOLT_OIDC_HYDRA_REMEMBER_FOR", "value", v, "err", perr.Error())
			os.Exit(1)
		}
		if parsed < 0 {
			logger.Error("RIVOLT_OIDC_HYDRA_REMEMBER_FOR must be >= 0", "value", v)
			os.Exit(1)
		}
		hydraRememberFor = parsed
	}
	if hydraClient.Enabled() {
		logger.Info("hydra remember_for configured", "duration", hydraRememberFor.String())
	}
	var userProvider idp.UserProvider
	if kratosClient.Enabled() {
		userProvider = idp.FromKratos(kratosClient)
		logger.Info("idp provisioning enabled", "backend", "kratos")
	} else {
		userProvider = idp.Disabled()
	}

	// OSRM same-origin proxy. RIVOLT_OSRM_BASE_URL points at the
	// cluster Service (e.g. http://osrm.osrm.svc.cluster.local).
	// Empty disables the feature; the SPA falls back to the public
	// OSRM demo via /api/config advertising an empty path.
	osrmProxy, err := maps.NewProxy(os.Getenv("RIVOLT_OSRM_BASE_URL"))
	if err != nil {
		logger.Error("osrm proxy init", "err", err.Error())
		os.Exit(1)
	}
	if osrmProxy != nil {
		logger.Info("osrm same-origin proxy enabled", "upstream", os.Getenv("RIVOLT_OSRM_BASE_URL"))
	}

	// Valhalla same-origin proxy. RIVOLT_VALHALLA_BASE_URL points
	// at the cluster Service serving the routing API (e.g.
	// http://valhalla.valhalla.svc.cluster.local). Mirrors the
	// OSRM proxy pattern: when set, /api/maps/valhalla/* forwards
	// to this URL and /api/config advertises the path so the SPA
	// can offer Valhalla as a routing-engine choice.
	valhallaProxy, err := maps.NewProxy(os.Getenv("RIVOLT_VALHALLA_BASE_URL"))
	if err != nil {
		logger.Error("valhalla proxy init", "err", err.Error())
		os.Exit(1)
	}
	if valhallaProxy != nil {
		logger.Info("valhalla same-origin proxy enabled", "upstream", os.Getenv("RIVOLT_VALHALLA_BASE_URL"))
	}

	// Backend-side Valhalla client. Used by the live recorder to
	// route-fill GPS-lag gaps in the live drive polyline so a dropped
	// fix sequence doesn't leave a straight-line shortcut across the
	// actual drive.
	if valhallaClient := maps.NewValhalla(os.Getenv("RIVOLT_VALHALLA_BASE_URL")); valhallaClient != nil && monitorRegistry != nil {
		monitorRegistry.SetRouteFiller(valhallaClient)
		logger.Info("recorder route-fill enabled", "backend", "valhalla")
	}

	// Tiles same-origin proxy. RIVOLT_TILES_BASE_URL points at the
	// cluster Service serving the .pmtiles bundle (nginx in front
	// of an NFS PVC, typically). Empty disables the feature; the
	// SPA falls back to CARTO's public dark raster basemap.
	tilesProxy, err := maps.NewProxy(os.Getenv("RIVOLT_TILES_BASE_URL"))
	if err != nil {
		logger.Error("tiles proxy init", "err", err.Error())
		os.Exit(1)
	}
	if tilesProxy != nil {
		logger.Info("tiles same-origin proxy enabled", "upstream", os.Getenv("RIVOLT_TILES_BASE_URL"))
	}

	// Photon geocoder. RIVOLT_PHOTON_BASE_URL points at the
	// in-cluster Service (e.g. http://photon.photon.svc.cluster.local).
	// Empty disables; /api/geocode then falls through to Open-Meteo
	// for city-level results without street-address support.
	photonClient := geocoding.NewPhotonClient(os.Getenv("RIVOLT_PHOTON_BASE_URL"))
	if photonClient.Enabled() {
		logger.Info("photon geocoder enabled", "upstream", photonClient.BaseURL)
	}

	// Invite-code store. Always wired when a DB is available so
	// the admin panel can generate codes even on existing installs.
	// nil-safe in api.Deps — disables the /api/signup and
	// /api/admin/invite-codes routes gracefully when no DB is present.
	var inviteStore *invites.Store
	if pgPool != nil {
		inviteStore = invites.New(pgPool)
	}

	// Signup-request store. Same nil-safe convention as invites; the
	// "request beta access" form on /signup and the admin review
	// surface stay unmounted when there is no DB.
	var signupRequestStore *signuprequests.Store
	if pgPool != nil {
		signupRequestStore = signuprequests.New(pgPool)
	}

	// Resend email client. Both vars must be set for the client to
	// actually send; otherwise the approval handler still works but
	// returns the invite code in the response so the admin can
	// forward it manually.
	mailer := email.New(email.Config{
		APIKey: os.Getenv("RIVOLT_RESEND_API_KEY"),
		From:   os.Getenv("RIVOLT_EMAIL_FROM"),
	})
	if mailer != nil {
		logger.Info("email sender enabled", "from", os.Getenv("RIVOLT_EMAIL_FROM"))
	}

	handler := api.New(api.Deps{
		Rivian:       rivianClient,
		Accounts:     accountRegistry,
		PushService:  pushSvc,
		Drives:       drivesFactory,
		Charges:      chargesFactory,
		Samples:      samplesFactory,
		Settings:     settingsFactory,
		Push:         pushFactory,
		Trips:        tripsFactory,
		Monitors:     monitorRegistry,
		SettingsMgr:  settingsMgr,
		Auth:         authSvc,
		AuthEnforced: authEnforced,
		OIDC:         oidcSvc,
		WebFS:        webFS,
		Version:      version,
		DB:           pgPool,
		Logger:       logger,
		Flags:        flagsStore,
		Secrets:      secretsStore,
		Metrics:      appMetrics,
		Users:           userProvider,
		Invites:         inviteStore,
		SignupRequests:  signupRequestStore,
		Email:           mailer,
		OSRMProxy:     osrmProxy,
		ValhallaProxy: valhallaProxy,
		TilesProxy:    tilesProxy,
		Photon:        photonClient,
		Hydra:            hydraClient,
		Kratos:           kratosClient,
		HydraRememberFor: hydraRememberFor,
	})

	// Wrap the chi router with otelhttp at the very outside. Span
	// is created here per request; the otelTraceRoute middleware
	// inside the chi chain renames it once the route pattern is
	// known. /api/health is excluded so the readiness/liveness
	// probes don't blow trace volume up — same reasoning as the
	// access-log skip.
	tracedHandler := otelhttp.NewHandler(handler, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/api/health" && r.URL.Path != "/metrics"
		}),
	)

	srv := &http.Server{
		Addr:    *addr,
		Handler: tracedHandler,
		// ReadHeaderTimeout guards against slow-loris clients that
		// open a TCP connection and dribble headers indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds the full request (headers + body). Most
		// endpoints have JSON bodies under 1 KB; the ElectraFi CSV
		// import is the outlier — multipart, capped at 1 GiB in the
		// handler, and big files happen at LAN speed, so 5 min is
		// generous without being abusable.
		ReadTimeout: 5 * time.Minute,
		// WriteTimeout bounds how long we hold a response open. AI
		// trip-analysis takes up to ~30 s; live WebSocket connections
		// use their own write deadlines internally. 5 min covers the
		// worst regular HTTP path with margin.
		WriteTimeout: 5 * time.Minute,
		// IdleTimeout closes idle keep-alive sockets so a misbehaving
		// client can't accumulate fds. Browsers reconnect cheaply.
		IdleTimeout: 90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http listening", "addr", *addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "err", err.Error())
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Release leases BEFORE the HTTP server shuts down so peers
	// can pick the vehicles up while we're still draining
	// in-flight requests. ReleaseAll uses a short bounded context;
	// a Postgres blip just delays acquisition until the TTL.
	if leaseCoordinator != nil {
		lctx, lcancel := context.WithTimeout(context.Background(), 3*time.Second)
		leaseCoordinator.Shutdown(lctx)
		lcancel()
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown error", "err", err.Error())
	}
	if pgPool != nil {
		_ = pgPool.Close()
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// parseLogLevel turns RIVOLT_LOG_LEVEL into a slog.Level. Defaults to
// Info on empty or unrecognised input — we'd rather log too much on a
// typo than silently log nothing.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// envFloat reads a float from an env var, or returns fallback if unset
// or unparseable. Used for numeric tunables we want to expose via env
// alongside a CLI flag.
func envFloat(name string, fallback float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// decodeHexSecret parses RIVOLT_COOKIE_SECRET. We require hex-
// encoding (rather than accepting a raw string) so operators can
// paste the output of `openssl rand -hex 32` without worrying about
// shell quoting of special characters, and so the length check in
// auth.New — which wants ≥32 bytes of entropy — is meaningful
// (a 32-char ASCII password decodes to 32 bytes of high-entropy
// key material; a 32-char hex string is only 16 bytes).
func decodeHexSecret(raw string) ([]byte, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("expected hex (e.g. openssl rand -hex 32): %w", err)
	}
	return b, nil
}

// postgresDSN returns the Postgres connection string, either from
// DATABASE_URL (takes precedence, for advanced DSN knobs) or
// assembled from discrete DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/
// DB_NAME/DB_SSLMODE so compose files don't have to embed the
// password twice. Returns "" when neither form is configured.
func postgresDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return dsn
	}
	host := strings.TrimSpace(os.Getenv("DB_HOST"))
	user := strings.TrimSpace(os.Getenv("DB_USER"))
	pass := os.Getenv("DB_PASSWORD")
	name := strings.TrimSpace(os.Getenv("DB_NAME"))
	if host == "" || user == "" || name == "" {
		return ""
	}
	port := strings.TrimSpace(os.Getenv("DB_PORT"))
	if port == "" {
		port = "5432"
	}
	sslmode := strings.TrimSpace(os.Getenv("DB_SSLMODE"))
	if sslmode == "" {
		sslmode = "disable"
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     host + ":" + port,
		Path:     "/" + name,
		RawQuery: "sslmode=" + url.QueryEscape(sslmode),
	}
	return u.String()
}

// loadOrCreateCookieSecret returns the 32-byte cookie-signing key
// stored at path, creating it on first call. The file is written
// 0o600 so only the rivolt user can read it; anyone with access to
// this file can forge session cookies (but they already have the
// whole database they'd be forging cookies to reach, so the blast
// radius is the same either way).
//
// Short files are rejected rather than silently padded — if the
// file got truncated by a botched copy we want to fail loud, not
// quietly downgrade security.
func loadOrCreateCookieSecret(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if len(b) < 32 {
			return nil, fmt.Errorf("%s is %d bytes, expected ≥32; delete it to regenerate", path, len(b))
		}
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	// WriteFile truncates+creates atomically enough for our use —
	// the only caller is single-threaded boot. If two replicas race
	// on a shared volume they'll each generate a secret and the
	// loser's gets overwritten; sessions issued between the two
	// boots stay valid under whichever key ultimately wins. A
	// proper HA story would use a config map / secret backend.
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return nil, fmt.Errorf("persist secret: %w", err)
	}
	return buf, nil
}

// buildSealer resolves the envelope-encryption KEK source from the
// environment. In production the operator sets RIVOLT_KEK to
// "<kekID>:<base64-32-bytes>" and optionally RIVOLT_KEK_ROTATION as
// a comma-separated list of retained old keys in the same format.
//
// The no-op path (RIVOLT_ALLOW_NOOP_SEALER=1) is a developer
// convenience only — it stores "ciphertext" that is plaintext with a
// magic header. Gated behind a deliberately long env var so it
// never accidentally ships; also logged at WARN so a mis-configured
// production instance is obvious in the very first log line.
func buildSealer(logger *slog.Logger) (rivoltcrypto.Sealer, error) {
	if os.Getenv("RIVOLT_ALLOW_NOOP_SEALER") == "1" {
		logger.Warn("RIVOLT_ALLOW_NOOP_SEALER=1 — secrets will NOT be encrypted at rest. Dev only.")
		return rivoltcrypto.NoopSealer{}, nil
	}
	rotation := []string{}
	if rot := strings.TrimSpace(os.Getenv("RIVOLT_KEK_ROTATION")); rot != "" {
		for _, v := range strings.Split(rot, ",") {
			if v = strings.TrimSpace(v); v != "" {
				rotation = append(rotation, v)
			}
		}
	}
	return rivoltcrypto.NewEnvSealerFromEnv("RIVOLT_KEK", rotation...)
}

// breakerMetrics adapts rivian.BreakerObserver onto the Prometheus
// gauge + counter on appMetrics. Kept in main.go (not the metrics
// package) so the metrics package stays import-free of rivian; the
// dependency direction is rivian → main → metrics, never the other
// way.
type breakerMetrics struct{ m *metrics.Metrics }

func (b *breakerMetrics) OnStateChange(_, to rivian.BreakerState) {
	if b == nil || b.m == nil {
		return
	}
	switch to {
	case rivian.BreakerClosed:
		b.m.RivianBreakerState.Set(0)
	case rivian.BreakerHalfOpen:
		b.m.RivianBreakerState.Set(1)
	case rivian.BreakerOpen:
		b.m.RivianBreakerState.Set(2)
	}
}

func (b *breakerMetrics) OnTrip(reason string) {
	if b == nil || b.m == nil {
		return
	}
	b.m.RivianBreakerTrips.WithLabelValues(reason).Inc()
}

// rateLimitMetrics wraps a *ratelimit.Limiter so we can observe
// every rejection without coupling the limiter package to
// internal/metrics. Kept here for the same dependency-direction
// reason as breakerMetrics.
type rateLimitMetrics struct {
	l *ratelimit.Limiter
	m *metrics.Metrics
}

func (r *rateLimitMetrics) Allow(ctx context.Context, class string) (bool, time.Duration) {
	ok, retry := r.l.Allow(ctx, ratelimit.Class(class))
	if !ok && r.m != nil {
		r.m.RivianRateLimitBlocked.WithLabelValues(class).Inc()
	}
	return ok, retry
}
