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
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	rivoltcrypto "github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/electrafi"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/web"
)

// version is stamped by the Docker build via -ldflags.
var version = "dev"

func main() {
	// Subcommand dispatch. Keeping this stdlib-only to avoid dragging
	// a dependency like cobra in for what is currently two commands.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import":
			runImport(os.Args[2:])
			return
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
  rivolt                       Start the HTTP server (default)
  rivolt import electrafi ...  Import TeslaFi/ElectraFi CSV dumps
  rivolt --help                Show this help

Environment:
  ADDR, DATA_DIR, VAPID_SUBJECT, OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY
  RIVIAN_CLIENT=stub|live|mock   (default: stub)
  RIVOLT_RESET_DATA=1            Wipe drives/charges/vehicle_state for the
                                 current user on boot, then continue. Scoped
                                 to the legacy "local" user; vehicles/settings/push
                                 are preserved. Unset after the first boot.
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

	logger.Info("rivolt starting",
		"version", version,
		"addr", *addr,
		"data_dir", *dataDir,
		"tz", time.Now().Location().String(),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		logger.Error("cannot create data dir", "path", *dataDir, "err", err.Error())
		os.Exit(1)
	}

	// Postgres is the only backend as of v0.4.2. The data dir still
	// holds auto-generated secrets (cookie_secret, VAPID keys) so
	// the volume mount stays.
	var pgPool *sql.DB
	var currentUserID uuid.UUID
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
		// Boot-time user-row seed for the legacy single-tenant
		// identity "local". This is what scopes settings/data when
		// no issuer (OIDC, trusted-proxy, bypass) is configured.
		// OIDC sign-in upserts its own user via EnsureUserFull.
		uid, err := db.EnsureUser(ctx, pgPool, "local")
		if err != nil {
			logger.Error("ensure user row failed", "err", err.Error())
			os.Exit(1)
		}
		currentUserID = uid
		logger.Info("postgres connected", "user_id", currentUserID.String())
	}

	settingsStore, err := settings.OpenStore(pgPool, currentUserID)
	if err != nil {
		logger.Warn("settings store unavailable", "err", err.Error())
	}

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
	if pgPool != nil {
		sealer, serr := buildSealer(logger)
		if serr != nil {
			logger.Error("sealer setup failed — refusing to start", "err", serr.Error())
			os.Exit(1)
		}
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
	var settingsMgr *settings.Manager
	if settingsStore != nil {
		seed := settings.AIConfig{
			OpenAIKey:    *openAIKey,
			AnthropicKey: *anthropicKey,
			GeminiKey:    *geminiKey,
		}
		settingsMgr, err = settings.NewManager(ctx, settingsStore, seed)
		if err != nil {
			logger.Warn("settings manager unavailable", "err", err.Error())
		}
	}

	pushStore, err := push.OpenStore(pgPool, currentUserID)
	if err != nil {
		logger.Warn("push store unavailable", "err", err.Error())
	}
	var pushSvc *push.Service
	if pushStore != nil {
		vapid, err := push.LoadOrGenerateVAPID(ctx, pushStore, *vapidPub, *vapidPriv, *vapidSubject)
		if err != nil {
			logger.Warn("VAPID setup failed", "err", err.Error())
		} else {
			pushSvc = push.NewService(pushStore, vapid, logger)
		}
	}

	var rivianClient rivian.Client
	var rivianAccount rivian.Account
	switch clientMode := os.Getenv("RIVIAN_CLIENT"); clientMode {
	case "mock":
		mc := rivian.NewMock()
		// Mock starts logged-out; the UI sign-in panel drives Login()
		// just like the live client. Per-user session hydration from
		// `secrets` happens lazily on the first authenticated request
		// via rivianHydrateMW — see internal/api/rivian_hydrate.go.
		// That keeps the boot path out of the per-user data plane,
		// which is the precondition for multi-user / multi-replica.
		if secretsStore == nil {
			logger.Info("rivian client: mock (no secrets store; login state will not persist)")
		} else {
			logger.Info("rivian client: mock (awaiting login)")
		}
		rivianClient = mc
		rivianAccount = mc
	case "stub":
		rivianClient = rivian.NewStub()
		logger.Info("rivian client: stub (no network)")
	default:
		// Live is the default. Auth happens later via Settings; the
		// server comes up fine without credentials, and Vehicles/State
		// just return a 'not authenticated' error until the user logs
		// in.
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
		// Persist needs_reauth transitions to Postgres so the
		// flag survives restarts and is visible to the Settings
		// UI's "re-authenticate" banner. The sink runs off the
		// hot path (only on the true→false edges); best-effort
		// persistence — failure is logged but doesn't mask the
		// original upstream error.
		lc.WithReauthSink(func(sinkCtx context.Context, reason string) {
			if err := db.SetNeedsReauth(sinkCtx, pgPool, currentUserID, reason); err != nil {
				logger.Warn("persist needs_reauth", "reason", reason, "err", err.Error())
			}
		})
		// Prime the in-memory mirror from Postgres at startup so
		// a crash-loop with stale creds doesn't briefly allow
		// requests until the first classification lands.
		if needs, reason, err := db.GetNeedsReauth(ctx, pgPool, currentUserID); err != nil {
			logger.Warn("load needs_reauth", "err", err.Error())
		} else if needs {
			lc.SetNeedsReauth(true, reason)
			logger.Info("rivian client: needs re-auth (from Postgres)", "reason", reason)
		}
		if secretsStore == nil {
			logger.Info("rivian client: live (no secrets store; login state will not persist)")
		} else {
			// Per-user Rivian session hydration is deferred to the first
			// authenticated request — see rivianHydrateMW. Booting without
			// a hydrated client is the right default for multi-user /
			// multi-replica: each replica lazy-loads from `user_secrets`
			// on demand, no coordination required.
			logger.Info("rivian client: live (awaiting login)")
		}
		rivianAccount = lc
		rivianClient = lc
	}

	// StateMonitor keeps a websocket subscription open per vehicle so
	// /api/state/:id can serve from cache instead of hammering the
	// GetVehicleState REST query. Only useful with the live client;
	// mock/stub don't have a websocket to subscribe to.
	var stateMonitor *rivian.StateMonitor
	if lc, ok := rivianClient.(*rivian.LiveClient); ok {
		stateMonitor = rivian.NewStateMonitor(lc, logger)
		// Start is deferred until after the stores are opened below
		// and wired via SetStores — otherwise the initial REST seed
		// fires before the recorder has anywhere to write.
	}

	vehiclesResolver := db.NewVehicleResolver(pgPool, currentUserID)

	drivesStore, err := drives.OpenStore(pgPool, currentUserID, vehiclesResolver)
	if err != nil {
		logger.Warn("drives store unavailable", "err", err.Error())
	}
	chargesStore, err := charges.OpenStore(pgPool, currentUserID, vehiclesResolver)
	if err != nil {
		logger.Warn("charges store unavailable", "err", err.Error())
	}
	samplesStore, err := samples.OpenStore(pgPool, currentUserID, vehiclesResolver)
	if err != nil {
		logger.Warn("samples store unavailable", "err", err.Error())
	}
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

	// Wire the stores into the monitor so live WS/REST frames get
	// persisted into vehicle_state / drives / charges. Without this
	// call the monitor is pure in-memory cache and live data is lost
	// on restart.
	if stateMonitor != nil {
		stateMonitor.SetStores(samplesStore, drivesStore, chargesStore)
		// Snapshot the home $/kWh rate at charge-close time so rate
		// edits don't retroactively rewrite billed history. Pulls
		// from the same settings store the UI writes to.
		if settingsStore != nil {
			stateMonitor.SetPriceLookup(func() (float64, string) {
				cfg, err := settings.GetChargingConfig(ctx, settingsStore)
				if err != nil {
					return 0, ""
				}
				return cfg.HomePricePerKWh, cfg.HomeCurrency
			})
		}
		stateMonitor.Start(ctx)

		// Prime per-vehicle metadata (model/trim/pack/image) in the
		// background. Best-effort — failure just means the SoC-delta
		// fallback uses DefaultPackKWh (131) and the UI has no
		// vehicle image to show. Don't block startup on Rivian's
		// gateway being available. Once metadata lands, kick off a
		// live-state subscription for every known vehicle so we
		// capture drives that happen while no browser is open — the
		// monitor used to only subscribe when /api/vehicles/{id}/state
		// was hit, which meant a car driven overnight recorded no
		// live samples.
		go func() {
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			if err := stateMonitor.RefreshVehicleInfo(rctx); err != nil {
				logger.Warn("vehicle info refresh failed", "err", err.Error())
				return
			}
			for _, v := range stateMonitor.AllVehicleInfo() {
				if v.ID == "" {
					continue
				}
				stateMonitor.EnsureSubscribed(v.ID)
			}
		}()
	}

	// One-time migration: v0.1.7 and earlier keyed ElectraFi charge/drive
	// rows by the CSV's chargeNumber/driveNumber counters, which reset
	// per export — re-importing an overlapping date range produced
	// duplicates. v0.1.8 switched to timestamp-based IDs; this pass
	// collapses any historical dupes by (vehicle_id, started_at).
	if drivesStore != nil {
		if n, err := drivesStore.Dedupe(ctx); err != nil {
			logger.Warn("drives dedupe failed", "err", err.Error())
		} else if n > 0 {
			logger.Info("drives dedupe", "removed", n)
		}
	}
	if chargesStore != nil {
		if n, err := chargesStore.Dedupe(ctx); err != nil {
			logger.Warn("charges dedupe failed", "err", err.Error())
		} else if n > 0 {
			logger.Info("charges dedupe", "removed", n)
		}
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
	// http:// homelab deployments where the browser will otherwise
	// refuse to store the session cookie.
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
		BypassUserID: bypassUserID,
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
	// header. Disabled when RIVOLT_OIDC_PROVIDERS is empty so the
	// homelab default ships zero behaviour change. When enabled
	// it requires pgPool (we need EnsureUserFull and a sessions
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
				return db.EnsureUserFull(ctx, pgPool, username, email, displayName)
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

	handler := api.New(api.Deps{
		Rivian:        rivianClient,
		RivianAccount: rivianAccount,
		SettingsStore: settingsStore,
		PushService:   pushSvc,
		PushStore:     pushStore,
		SettingsMgr:   settingsMgr,
		Drives:        drivesStore,
		Charges:       chargesStore,
		Samples:       samplesStore,
		StateMonitor:  stateMonitor,
		Auth:          authSvc,
		AuthEnforced:  authEnforced,
		OIDC:          oidcSvc,
		WebFS:         webFS,
		Version:       version,
		DB:            pgPool,
		Logger:        logger,
		Flags:         flagsStore,
		Secrets:       secretsStore,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
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
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown error", "err", err.Error())
	}
	if pushStore != nil {
		_ = pushStore.Close()
	}
	if settingsStore != nil {
		_ = settingsStore.Close()
	}
	if drivesStore != nil {
		_ = drivesStore.Close()
	}
	if chargesStore != nil {
		_ = chargesStore.Close()
	}
	if samplesStore != nil {
		_ = samplesStore.Close()
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
	// boots stay valid under whichever key ultimately wins. Good
	// enough for a homelab; a real HA story would use a config
	// map / secret backend.
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return nil, fmt.Errorf("persist secret: %w", err)
	}
	return buf, nil
}

// runImport dispatches "rivolt import <kind> ..." subcommands.
func runImport(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rivolt import electrafi <file.csv> [<file.csv>...]")
		os.Exit(2)
	}
	switch args[0] {
	case "electrafi":
		runImportElectraFi(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown import source %q\n", args[0])
		os.Exit(2)
	}
}

// runImportElectraFi imports one or more TeslaFi/ElectraFi CSV dumps.
func runImportElectraFi(args []string) {
	fs := flag.NewFlagSet("import electrafi", flag.ExitOnError)
	vehicleID := fs.String("vehicle-id", envOr("RIVOLT_VEHICLE_ID", ""), "vehicle_id to attribute sessions to (default: derived from filename)")
	packKWh := fs.Float64("pack-kwh", envFloat("RIVOLT_PACK_KWH", electrafi.DefaultPackKWh), "usable pack capacity in kWh; used to estimate energy when ElectraFi omits charger_power")
	tz := fs.String("tz", envOr("RIVOLT_IMPORT_TZ", "Local"), "IANA timezone the CSV timestamps were recorded in (e.g. America/New_York); 'Local' uses the host's zone, 'UTC' keeps the pre-v0.4.2 behavior")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	files := fs.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rivolt import electrafi <file.csv> [<file.csv>...]")
		os.Exit(2)
	}

	dsn := postgresDSN()
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL (or DB_HOST/DB_USER/DB_PASSWORD/DB_NAME) is required")
		os.Exit(1)
	}
	ctx := context.Background()
	pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	pool, err := db.Open(pctx, dsn)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres open: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()
	// Single-tenant identity. Imports always land on "local" \u2014
	// override via RIVOLT_IMPORT_USER for multi-user setups.
	username := strings.TrimSpace(os.Getenv("RIVOLT_IMPORT_USER"))
	if username == "" {
		username = "local"
	}
	uid, err := db.EnsureUser(ctx, pool, username)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ensure user: %v\n", err)
		os.Exit(1)
	}
	resolver := db.NewVehicleResolver(pool, uid)

	ds, err := drives.OpenStore(pool, uid, resolver)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open drives: %v\n", err)
		os.Exit(1)
	}
	defer ds.Close()
	cs, err := charges.OpenStore(pool, uid, resolver)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open charges: %v\n", err)
		os.Exit(1)
	}
	defer cs.Close()
	ss, err := samples.OpenStore(pool, uid, resolver)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open samples: %v\n", err)
		os.Exit(1)
	}
	defer ss.Close()

	loc, err := time.LoadLocation(strings.TrimSpace(*tz))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --tz %q: %v\n", *tz, err)
		os.Exit(2)
	}
	imp := &electrafi.Importer{Drives: ds, Charges: cs, Samples: ss, VehicleID: *vehicleID, PackKWh: *packKWh, Location: loc}
	var totalRows, totalSamples, totalDrives, totalCharges, totalSkipped int
	for _, f := range files {
		res, err := imp.Import(ctx, f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "import %s: %v\n", f, err)
			os.Exit(1)
		}
		fmt.Printf("%s: rows=%d samples=%d drives=%d charges=%d skipped=%d\n",
			f, res.Rows, res.Samples, res.Drives, res.Charges, res.SkippedRows)
		totalRows += res.Rows
		totalSamples += res.Samples
		totalDrives += res.Drives
		totalCharges += res.Charges
		totalSkipped += res.SkippedRows
	}
	fmt.Printf("total: rows=%d samples=%d drives=%d charges=%d skipped=%d\n",
		totalRows, totalSamples, totalDrives, totalCharges, totalSkipped)
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
