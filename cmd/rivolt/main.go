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
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/electrafi"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
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
`)
}

func runServer() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		p, err := db.Open(pctx, dsn)
		cancel()
		if err != nil {
			logger.Error("postgres open failed", "err", err.Error())
			os.Exit(1)
		}
		pgPool = p
		u := strings.TrimSpace(os.Getenv("RIVOLT_USERNAME"))
		if u == "" {
			// No login configured — still need a user row to scope
			// settings against. Use a well-known "local" identity;
			// it's just a UUID salt, it isn't displayed anywhere.
			u = "local"
		}
		uid, err := db.EnsureUser(ctx, pgPool, u)
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
		// The mock now speaks the same Account surface as live —
		// start logged-out so the UI shows the sign-in panel.
		// Any email+password authenticates; "mfa" in the email
		// triggers the MFA flow; "fail" rejects.
		if settingsStore != nil {
			if sess, err := settings.LoadRivianSession(ctx, settingsStore); err != nil {
				logger.Warn("restore rivian session", "err", err.Error())
			} else if sess.UserSessionToken != "" {
				mc.Restore(sess)
				logger.Info("rivian client: mock (restored session)", "email", sess.Email)
			} else {
				logger.Info("rivian client: mock (awaiting Settings login)")
			}
		} else {
			logger.Info("rivian client: mock (no settings store; login state will not persist)")
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
		lc := rivian.NewLive()
		if settingsStore != nil {
			if sess, err := settings.LoadRivianSession(ctx, settingsStore); err != nil {
				logger.Warn("restore rivian session", "err", err.Error())
			} else if sess.UserSessionToken != "" {
				lc.Restore(sess)
				logger.Info("rivian client: live (restored session)", "email", sess.Email)
			} else {
				logger.Info("rivian client: live (awaiting Settings login)")
			}
		} else {
			logger.Info("rivian client: live (no settings store; login state will not persist)")
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
		// gateway being available.
		go func() {
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			if err := stateMonitor.RefreshVehicleInfo(rctx); err != nil {
				logger.Warn("vehicle info refresh failed", "err", err.Error())
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

	// Auth is opt-in via env. Leaving RIVOLT_USERNAME / RIVOLT_PASSWORD
	// unset keeps the legacy single-tenant UX — every /api/* route
	// stays open, exactly like v0.3.x. Setting them flips the router
	// to cookie-gated mode; the homelab login page renders and
	// /api/* requires a session.
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
	authEnabled := os.Getenv("RIVOLT_USERNAME") != "" && os.Getenv("RIVOLT_PASSWORD") != ""
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
	authSvc, err := auth.New(auth.Config{
		Username:          os.Getenv("RIVOLT_USERNAME"),
		Password:          os.Getenv("RIVOLT_PASSWORD"),
		CookieSecret:      cookieSecret,
		SecureCookie:      secureCookie,
		TrustedProxyCIDRs: trustedNets,
		UserIDFor:         db.UserIDFor,
	}, func() (uuid.UUID, error) {
		// With Postgres wired we do a real upsert so the users row
		// is present for future FK references (charges.user_id,
		// etc.). Without it we fall back to the deterministic v5
		// hash — the UUID is stable either way, the upsert is the
		// only thing that needs a backend.
		if pgPool != nil {
			return db.EnsureUser(ctx, pgPool, os.Getenv("RIVOLT_USERNAME"))
		}
		return db.UserIDFor(os.Getenv("RIVOLT_USERNAME")), nil
	})
	if err != nil {
		logger.Error("auth init", "err", err.Error())
		os.Exit(1)
	}
	if authEnabled {
		logger.Info("auth enabled",
			"username", os.Getenv("RIVOLT_USERNAME"),
			"trusted_cidrs", len(trustedNets),
			"secure_cookie", secureCookie,
			"cookie_secret", cookieSecretSource,
		)
	} else {
		logger.Warn("auth disabled — set RIVOLT_USERNAME and RIVOLT_PASSWORD to require login")
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
		WebFS:         webFS,
		Version:       version,
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
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	pool, err := db.Open(pctx, dsn)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres open: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()
	username := strings.TrimSpace(os.Getenv("RIVOLT_USERNAME"))
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
