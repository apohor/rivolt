// Command rivolt is the single-binary server for the Rivolt Rivian companion.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	// Embed the IANA time zone database so TZ=America/New_York etc. work
	// even on distroless images that don't ship /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/adminaudit"
	"github.com/apohor/rivolt/internal/adminhealth"
	"github.com/apohor/rivolt/internal/adminonly"
	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/appauth"
	"github.com/apohor/rivolt/internal/appleid"
	"github.com/apohor/rivolt/internal/appsettings"
	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/bg"
	"github.com/apohor/rivolt/internal/cfaccess"
	"github.com/apohor/rivolt/internal/changelog"
	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/charges"
	rivoltcrypto "github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/drain"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/elevation"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/identity"
	"github.com/apohor/rivolt/internal/idp"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/leftopen"
	"github.com/apohor/rivolt/internal/logging"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/milestones"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/onboardmail"
	"github.com/apohor/rivolt/internal/otadetails"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/passkeys"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/plugreminder"
	"github.com/apohor/rivolt/internal/push"
	"github.com/apohor/rivolt/internal/ratelimit"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/retention"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/rivianexpiry"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/signupgate"
	"github.com/apohor/rivolt/internal/signuprequests"
	"github.com/apohor/rivolt/internal/statement"
	"github.com/apohor/rivolt/internal/syntheticplan"
	"github.com/apohor/rivolt/internal/tracing"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/weather"
	"github.com/apohor/rivolt/internal/web"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/redis/go-redis/v9"
)

// version is stamped by the Docker build via -ldflags. The default
// "dev" is overridden per build; releases are cut by tagging a
// commit that already has a successful build (see the tag-validate
// gate in .github/workflows/build.yml).
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h", "help":
			printUsage()
			return
		case "migrate":
			os.Exit(runMigrate(os.Args[2:]))
		case "reprice":
			os.Exit(runReprice(os.Args[2:]))
		case "repair-charge-fragments":
			os.Exit(runRepairChargeFragments(os.Args[2:]))
		case "repair-charge-energy":
			os.Exit(runRepairChargeEnergy(os.Args[2:]))
		case "repair-phantom-charges":
			os.Exit(runRepairPhantoms(os.Args[2:]))
		case "repair-charge-blips":
			os.Exit(runRepairBlips(os.Args[2:]))
		case "repair-r2-history":
			os.Exit(runRepairR2History(os.Args[2:]))
		case "announce":
			os.Exit(runAnnounce(os.Args[2:]))
		case "archive":
			os.Exit(runArchive(os.Args[2:]))
		}
	}
	runServer()
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `rivolt — self-hosted Rivian companion

Usage:
  rivolt          Start the HTTP server (default)
  rivolt --help   Show this help
  rivolt repair-charge-fragments [--apply] [--user NAME] [--since T] [--until T]
                  [--max-gap 200s] [--backup FILE]
                  Merge back-to-back pieces of one charging session that
                  carry a running energy total into one row. Dry run
                  unless --apply; --apply needs --user/--since/--until
                  and writes a CSV backup first.
  rivolt migrate [--check]    apply pending database migrations (or list them); for deployments that migrate in a job
  rivolt reprice [--user NAME] [--ids FILE] [--detect [--min-confidence X]] [--apply --backup FILE] | --restore-csv FILE
                  Classify legacy charge prices and re-price DC fast
                  charges stored at the home rate (dry run unless --apply)
  rivolt repair-charge-energy [--since DATE] [--ids-out FILE]            (dry run: counts per model)
  rivolt repair-charge-energy --apply --ids FILE --user NAME --backup FILE
  rivolt repair-charge-energy --restore-json FILE [--apply]
  rivolt repair-r2-history --vehicle ID --since T --until T [--apply --backup CSV]
  rivolt repair-r2-history --vehicle ID --restore-csv FILE [--apply]
                  Rebuild a vehicle's drives and charges in a window
                  from its samples (R2s recorded before park/sleep came
                  from Parallax). Dry run unless --apply; CSV backup
                  first; one transaction; never migrates.
  rivolt announce --subject S --body-file FILE [--apply] [--sent-log FILE]
                  [--test-to USER_ID] [--exclude-user ID|NAME ...] [--rate 2]
                  One-time service email to active users through the
                  configured mailer, one message each. Dry run lists
                  user id prefixes only; --apply sends and records each
                  user id in --sent-log so a re-run resumes; --test-to
                  sends to one user. Never migrates; never prints emails.
  rivolt repair-phantom-charges --ids FILE [--apply --backup CSV]
  rivolt repair-phantom-charges --restore BATCH | --restore-csv FILE [--apply]
  rivolt repair-charge-blips [--ids-out FILE]                              (dry run: counts and the id-list sha256)
  rivolt repair-charge-blips --apply --expect-count N --expect-sha SHA --backup CSV
  rivolt repair-charge-blips --restore BATCH | --restore-csv FILE [--apply]
                  Delete audited phantom live charges (dry run unless
                  --apply; --user required; CSV backup first; never
                  migrates the database; --restore/--restore-csv undo)

Environment:
  RIVOLT_OIDC_TRUST_UNVERIFIED_EMAIL=true
                                 let an email the IdP did not verify be an
                                 account key (unsafe with permissive IdPs).
                                 Default false.
  RIVOLT_TRUST_FIRST_PARTY_EMAILS=false
                                 count Rivolt's own Kratos/Hydra emails as
                                 verified only if Kratos verified them (for
                                 open-registration Kratos). Default true.
  RIVOLT_SIGNUP_DISABLED=true    close sign-up: no /signup page or API, no
                                 access requests, and a first sign-in by an
                                 unknown OIDC/Kratos identity is refused.
                                 Existing and admin-created users, the
                                 RIVOLT_BOOTSTRAP_ADMIN_EMAIL address and the
                                 first user of an empty install still work.
                                 Default false.
  ADDR, DATA_DIR, VAPID_SUBJECT, OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY
  RIVIAN_CLIENT=stub|live|mock|replay   (default: stub)
  RIVOLT_REPLAY_SOURCE=sample    replay: use the generated sample truck
                                 (cmd/sampletruck) instead of a recorded
                                 vehicle. Off unless set.
  RIVOLT_REPLAY_SOURCE_VEHICLE   replay: Rivian id of the vehicle whose
                                 recorded history demo vehicles replay.
  RIVOLT_REPLAY_FROM/_TO         replay: optional corpus bounds (RFC 3339
                                 or YYYY-MM-DD).
  RIVOLT_REPLAY_BACKFILL         replay: history recorded on first connect
                                 (Go duration, default 720h).
  RIVOLT_REPLAY_ALLOWED_USERS    replay: comma-separated usernames allowed a
                                 demo vehicle; "*" for everyone (default:
                                 nobody).
  RIVOLT_RIVIAN_LIVE_FORBIDDEN=true  Refuse to boot with a live Rivian
                                 client (preview: replay/mock/stub only).
  RIVOLT_DEMO_TILES_PER_CLIENT_PER_MIN / _GLOBAL_PER_MIN
                                 demo: anonymous map-tile budgets per
                                 client and per install, requests/min
                                 (default 1200 / 12000).
  RIVOLT_RESET_DATA=1            Wipe drives/charges/vehicle_state for the
                                 legacy "local" user on boot, then continue.
                                 Vehicles/settings/push are preserved.
`)
}

// replayRedisPrefixOK reports whether replay mode may use the shared
// Redis live-state store under prefix. Replay frames must never land in
// prod's keyspace: an unset prefix defaults to "rivolt", prod's, so
// replay requires an explicit, different one.
func replayRedisPrefixOK(prefix string) bool {
	p := strings.TrimSpace(prefix)
	return p != "" && p != "rivolt"
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

	// Standalone public demo (RIVOLT_DEMO_ONLY): refuse a half-configured
	// install, then make sure no sign-in, mail, push or AI subsystem can
	// come up from stray environment, before any flag default reads it.
	if demo.StrayOnly() {
		logger.Warn("RIVOLT_DEMO_ONLY is set but RIVOLT_DEMO is not: IGNORING it; this is not a demo install. Remove the variable.")
	}
	if demo.MisconfiguredOnly() {
		logger.Error("RIVOLT_DEMO_ONLY needs the replay sample demo (RIVOLT_DEMO=true, RIVIAN_CLIENT=replay, RIVOLT_REPLAY_SOURCE=sample, no RIVOLT_REPLAY_SOURCE_VEHICLE)")
		os.Exit(1)
	}
	if demo.OnlyEnabled() {
		demoOnlyScrubEnv(logger)
	}

	// Admin deployment (RIVOLT_ADMIN_ONLY): same image, admin surface only,
	// behind Cloudflare Access, no background work. Refuse a half-configured
	// install, and build the Access verifier now so a bad team domain or an
	// empty allowlist stops the boot.
	adminOnlyMode := adminonly.Enabled(os.Getenv)
	var accessVerifier *cfaccess.Verifier
	if adminOnlyMode {
		if problems := adminonly.Problems(os.Getenv); len(problems) > 0 {
			for _, p := range problems {
				logger.Error("RIVOLT_ADMIN_ONLY: " + p)
			}
			os.Exit(1)
		}
		v, err := cfaccess.New(cfaccess.Config{
			TeamDomain: os.Getenv("RIVOLT_ACCESS_TEAM_DOMAIN"),
			Aud:        os.Getenv("RIVOLT_ACCESS_AUD"),
			Emails:     cfaccess.ParseEmails(os.Getenv("RIVOLT_ADMIN_EMAILS")),
			// Optional, for automation (QA on preview). Never set on prod.
			ServiceTokens: cfaccess.ParseList(os.Getenv("RIVOLT_ACCESS_SERVICE_TOKENS")),
		}, logger)
		if err != nil {
			logger.Error("RIVOLT_ADMIN_ONLY: cloudflare access config", "err", err.Error())
			os.Exit(1)
		}
		accessVerifier = v
		if strings.TrimSpace(os.Getenv("RIVOLT_PUBLIC_BASE_URL")) == "" {
			logger.Warn("admin-only: RIVOLT_PUBLIC_BASE_URL is unset; signup links in approval emails will point at this admin host, which users can't open",
				"base_url", os.Getenv("RIVOLT_BASE_URL"))
		}
		if st := cfaccess.ParseList(os.Getenv("RIVOLT_ACCESS_SERVICE_TOKENS")); len(st) > 0 {
			logger.Warn("admin-only: Access service tokens are accepted at the outer door (automation); prod must not set this", "tokens", len(st))
		}
		logger.Info("admin-only: serving the admin surface behind Cloudflare Access; background work is off")
	}

	// User-facing links the server composes (the signup link in approval
	// emails) use the public site's origin. RIVOLT_BASE_URL is this host's
	// own origin (OIDC callbacks); on the admin deployment the two differ,
	// so RIVOLT_PUBLIC_BASE_URL names the public site there.
	publicBaseURL := firstNonEmpty(os.Getenv("RIVOLT_PUBLIC_BASE_URL"), os.Getenv("RIVOLT_BASE_URL"))

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

	memLimit := applyMemoryLimit()
	logger.Info("rivolt starting",
		"version", version,
		"addr", *addr,
		"data_dir", *dataDir,
		"tz", time.Now().Location().String(),
		"go_memory_limit", memLimit,
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
		// OpenWithRetry first waits up to 60 s for Postgres to be
		// reachable (a fresh pod can lack a route for a moment), and
		// the listener below is not up until it returns, so readiness
		// stays false meanwhile.
		pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		p, err := db.OpenWithRetry(pctx, dsn, 60*time.Second, logger)
		cancel()
		if err != nil {
			logger.Error("postgres open failed", "err", err.Error())
			os.Exit(1)
		}
		pgPool = p
		logger.Info("postgres connected")
		// Read-only reporting role (Grafana's Postgres datasource):
		// SELECT on an allowlist of non-secret tables. The role is
		// created by CNPG on its own schedule, so re-apply after boot
		// too; grants are idempotent and cheap. See internal/db/readonly.go.
		// The request role (row-level security, stage 1): its grants only. Nothing
		// connects as it yet; stage 2 gives request handling a pool of its own.
		// Re-applied like the reporting role's, since CNPG creates it on its own
		// schedule and a later migration's table needs the narrowing. See
		// internal/db/app_role.go.
		if role := os.Getenv("RIVOLT_APP_ROLE"); role != "" && !adminOnlyMode {
			go func() {
				t := time.NewTicker(6 * time.Hour)
				defer t.Stop()
				for {
					gctx, gcancel := context.WithTimeout(ctx, 30*time.Second)
					db.EnsureAppGrants(gctx, pgPool, role, logger)
					gcancel()
					select {
					case <-ctx.Done():
						return
					case <-t.C:
					}
				}
			}()
		}
		if role := os.Getenv("RIVOLT_READONLY_ROLE"); role != "" && !adminOnlyMode {
			go func() {
				t := time.NewTicker(6 * time.Hour)
				defer t.Stop()
				for {
					gctx, gcancel := context.WithTimeout(ctx, 30*time.Second)
					db.EnsureReadonlyGrants(gctx, pgPool, role, logger)
					gcancel()
					select {
					case <-ctx.Done():
						return
					case <-t.C:
					}
				}
			}()
		}
	}

	// appMetrics owns the Prometheus registry. Built before the request pool (its no-user counter), the rivian client
	// (the breaker observer) and the lease coordinator (the leases gauge), so each can wire in at construction.
	appMetrics := metrics.New()
	appMetrics.RegisterDBPool("system", pgPool)
	if pgPool != nil {
		n, why := db.SystemPoolSize(os.Getenv)
		logger.Info("system database pool", "max_conns", n, "because", why)
	}

	// Row-level security, stage 2: the pool request handling uses. Unset (the default) it is pgPool itself; see requestpool.go.
	// pgPool stays the system pool: the monitor and recorder, background jobs, admin and pre-auth paths.
	requestDB, closeRequestDB, rerr := openRequestSide(ctx, os.Getenv, pgPool, func(c context.Context) {
		appMetrics.RequestAcquiresWithoutUser.WithLabelValues(api.RoutePatternFromContext(c)).Inc()
	}, logger)
	if rerr != nil {
		logger.Error("request pool", "err", rerr.Error())
		os.Exit(1)
	}
	defer closeRequestDB()

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
	var secretsStore *secrets.Store // system pool (the monitor, auth, admin); the request-side one is in reqStores
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

	// The stores request handling uses, on the request pool (the same *sql.DB as pgPool when RIVOLT_REQUEST_DATABASE_URL is unset).
	// The monitor, recorder and jobs keep the system-pool ones above. The pool-on integration tests build theirs the same way.
	reqStores := api.NewRequestStores(requestDB, pgPool, sealer)

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
	if pgPool != nil && !demo.OnlyEnabled() {
		serverPushStore := push.NewServerStore(pgPool)
		vapid, verr := push.LoadOrGenerateVAPID(ctx, serverPushStore, *vapidPub, *vapidPriv, *vapidSubject)
		if verr != nil {
			logger.Warn("VAPID setup failed", "err", verr.Error())
		} else {
			pushSvc = push.NewService(serverPushStore, vapid, logger)
			// The iPhone app's push (Apple Push Notification service). Off unless
			// all four RIVOLT_APNS_* values are set; an incomplete or unusable
			// key is logged and leaves it off rather than stopping the server.
			apnsCfg := push.APNsConfig{
				KeyID: os.Getenv("RIVOLT_APNS_KEY_ID"), TeamID: os.Getenv("RIVOLT_APNS_TEAM_ID"),
				KeyPEM: os.Getenv("RIVOLT_APNS_KEY"), Topic: os.Getenv("RIVOLT_APNS_TOPIC"),
			}
			if apnsCfg.Enabled() {
				if a, aerr := push.NewAPNs(apnsCfg); aerr != nil {
					logger.Warn("APNs disabled", "err", aerr.Error())
				} else {
					pushSvc.WithAPNs(a)
					logger.Info("APNs enabled", "topic", a.Topic())
					// The charging Live Activity: off unless RIVOLT_APNS_LIVE is set.
					if live := strings.EqualFold(os.Getenv("RIVOLT_APNS_LIVE"), "true") || os.Getenv("RIVOLT_APNS_LIVE") == "1"; live {
						pushSvc.WithLive(true)
						logger.Info("APNs live activities enabled")
					}
				}
			}
			// Notification text is kept 24 hours for the app to fetch; sweep it hourly.
			go func() {
				t := time.NewTicker(time.Hour)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						pctx, pcancel := context.WithTimeout(ctx, time.Minute)
						if n, perr := push.PurgeNotifications(pctx, pgPool); perr != nil {
							logger.Warn("push notifications sweep failed", "err", perr.Error())
						} else if n > 0 {
							logger.Info("push notifications sweep", "deleted", n)
						}
						if n, perr := push.PurgeActivities(pctx, pgPool); perr != nil {
							logger.Warn("push activities sweep failed", "err", perr.Error())
						} else if n > 0 {
							logger.Info("push activities sweep", "deleted", n)
						}
						pcancel()
					}
				}
			}()
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
	// redisUp is true once sharedRedis has answered: at boot, or later when the
	// retrying limiter connects. The live-state store is gated on it, so it can be
	// installed before Redis is reachable and start working when it is.
	var redisUp atomic.Bool
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
	db.ServerEventObserver = func(event, outcome string) { appMetrics.ServerEventsTotal.WithLabelValues(event, outcome).Inc() }
	rivian.RecorderWriteFailureObserver = func(kind string) { appMetrics.RecorderWriteFailuresTotal.WithLabelValues(kind).Inc() }
	rivian.WSFrameDropObserver = appMetrics.WSFramesDroppedTotal.Inc
	// /metrics is served only on its own listener, never on the public
	// router: the Ingress routes every path to the main port, so a
	// /metrics route there is public (counts, route names, rate-limit
	// state). RIVOLT_METRICS_ADDR sets the address (default :9090); "off"
	// disables the listener.
	metricsAddr := strings.TrimSpace(os.Getenv("RIVOLT_METRICS_ADDR"))
	if metricsAddr == "" {
		metricsAddr = ":9090"
	}
	if strings.EqualFold(metricsAddr, "off") {
		metricsAddr = ""
	}
	// Resend email client. Both vars must be set for the client to
	// actually send; otherwise the approve handler still returns the
	// magic-link in the response so the admin can forward it manually.
	// Built here (ahead of the rivian client) so the per-user reauth
	// sink can capture it and email a user whose Rivian session died.
	mailer := email.New(email.Config{
		APIKey:  os.Getenv("RIVOLT_RESEND_API_KEY"),
		From:    os.Getenv("RIVOLT_EMAIL_FROM"),
		Counter: emailMetricsAdapter{m: appMetrics},
	})
	if mailer != nil {
		logger.Info("email sender enabled", "from", os.Getenv("RIVOLT_EMAIL_FROM"))
	}
	// Install-wide Redis key prefix. Defaults to "rivolt"; the preview
	// environment sets "rivolt-preview" so preview and prod can share
	// one Redis without colliding on the rate-limiter token buckets
	// (per-user livestate keys are already disjoint by user_id, but
	// the limiter classes are install-wide constants). Hoisted out
	// of the rivian-client switch so the livestate factory below
	// can read the same value.
	// RIVOLT_REPLICA_DIVISOR is how many replicas may run at once (replicas plus the rolling-update
	// surge; the chart sets it). While Redis cannot answer, the Rivian call budget and the planner
	// quota each fall back to 1/this of the shared limit per pod, so the pods together stay inside it.
	replicaDivisor := replicaDivisorFromEnv(os.Getenv)
	logger.Info("redis fallback limits", "replica_divisor", replicaDivisor)
	redisKeyPrefix := strings.TrimSpace(os.Getenv("RIVOLT_REDIS_KEY_PREFIX"))
	if redisKeyPrefix == "" {
		redisKeyPrefix = "rivolt"
	}
	// purgeUserCaches deletes what this install holds of a deleted user in Redis (the live-state
	// snapshots, which carry coordinates, for up to two hours): after an account deletion here, and
	// when this pod learns of one made on another pod. Best effort; no Redis, nothing to do.
	purgeUserCaches := func(uid uuid.UUID) {
		if sharedRedis == nil {
			return
		}
		(&rivian.LiveStatePurger{Rdb: sharedRedis, Prefix: redisKeyPrefix, Log: logger}).Purge(ctx, uid.String())
	}
	// Fail closed on installs that must never talk to Rivian (preview:
	// a second live subscriber on a VIN prod also streams kicks both
	// off and split-brains the recorder). Anything but the network-free
	// modes is the live client, so refuse to boot rather than rely on
	// the kill switch or a dead session to keep it quiet.
	if rivian.ParseBoolEnv(os.Getenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN")) {
		switch os.Getenv("RIVIAN_CLIENT") {
		case "replay", "mock", "stub":
		default:
			logger.Error("RIVOLT_RIVIAN_LIVE_FORBIDDEN is set; RIVIAN_CLIENT must be replay, mock or stub",
				"rivian_client", os.Getenv("RIVIAN_CLIENT"))
			os.Exit(1)
		}
	}
	// The replay client's trip planner: stored plans first, then a synthetic
	// plan from Valhalla and the charger archive (both wired further down,
	// before serving, when they are configured).
	// The demo is a replay install too and gets this planner, but nothing
	// reaches it there: /api/trips/plan* is not in the demo allowlist (and the
	// demo has no Valhalla), so "the demo plans from stored plans only" holds
	// by that gating, not by construction. Keep it out of the allowlist.
	var tripSynth *syntheticplan.Planner
	switch clientMode := os.Getenv("RIVIAN_CLIENT"); clientMode {
	case "replay":
		// Demo vehicles: every signed-in user who "connects Rivian"
		// (any credentials) gets one vehicle that replays a recorded
		// vehicle's history, time-shifted to now, through the real
		// recorder. No LiveClient is ever built in this mode.
		backfill := 30 * 24 * time.Hour
		if v := strings.TrimSpace(os.Getenv("RIVOLT_REPLAY_BACKFILL")); v != "" {
			var err error
			if backfill, err = time.ParseDuration(v); err != nil {
				logger.Error("bad RIVOLT_REPLAY_BACKFILL", "err", err.Error())
				os.Exit(1)
			}
		}
		var feed *rivian.ReplayFeed
		src := strings.TrimSpace(os.Getenv("RIVOLT_REPLAY_SOURCE_VEHICLE"))
		if strings.TrimSpace(os.Getenv("RIVOLT_REPLAY_SOURCE")) == "sample" {
			// The generated sample truck (cmd/sampletruck): no recorded
			// vehicle and no real person's data behind it.
			feed = rivian.NewSampleReplayFeed(pgPool, backfill, logger)
			src = "sample"
		} else {
			if pgPool == nil || src == "" {
				logger.Error("RIVIAN_CLIENT=replay needs DATABASE_URL and RIVOLT_REPLAY_SOURCE_VEHICLE (or RIVOLT_REPLAY_SOURCE=sample)")
				os.Exit(1)
			}
			from, err := parseReplayBound(os.Getenv("RIVOLT_REPLAY_FROM"))
			if err != nil {
				logger.Error("bad RIVOLT_REPLAY_FROM", "err", err.Error())
				os.Exit(1)
			}
			to, err := parseReplayBound(os.Getenv("RIVOLT_REPLAY_TO"))
			if err != nil {
				logger.Error("bad RIVOLT_REPLAY_TO", "err", err.Error())
				os.Exit(1)
			}
			feed, err = rivian.NewDBReplayFeed(ctx, pgPool, src, from, to, backfill, logger)
			if err != nil {
				logger.Error("replay feed", "err", err.Error())
				os.Exit(1)
			}
		}
		// The corpus is a real vehicle's history, and preview shares
		// sign-in with prod: demo vehicles go only to listed usernames.
		// Fails closed: unset means nobody; "*" opts everyone in.
		if v := strings.TrimSpace(os.Getenv("RIVOLT_REPLAY_ALLOWED_USERS")); v != "*" {
			allowed := map[uuid.UUID]bool{}
			for _, u := range strings.Split(v, ",") {
				if u = strings.TrimSpace(u); u != "" {
					allowed[db.UserIDFor(u)] = true
				}
			}
			if len(allowed) == 0 {
				logger.Warn("RIVOLT_REPLAY_ALLOWED_USERS is empty; no user can connect a demo vehicle")
			}
			// The public demo account (RIVOLT_DEMO, preview only) is
			// always allowed its replay vehicle.
			if demo.Enabled() {
				allowed[demo.UserID()] = true
			}
			feed.SetAllowedUsers(func(uid uuid.UUID) bool { return allowed[uid] })
		}
		fixtures, ferr := demo.PlanFixtures()
		if ferr != nil {
			logger.Warn("replay: no stored trip plans", "err", ferr.Error())
		}
		tripSynth = &syntheticplan.Planner{Vehicle: syntheticplan.SampleVehicle(), Now: time.Now}
		accountRegistry = rivian.NewReplayAccountRegistry(feed, rivian.FirstPlanner{rivian.NewFixturePlanner(fixtures, nil), tripSynth})
		rivianClient = rivian.NewStub()
		logger.Info("rivian client: replay (no network)", "source_vehicle", src)
		// Share live state across replicas as live mode does, so a pod
		// that doesn't own the replay vehicle serves the owner's published
		// /api/state rather than a one-off fetch of its own. Redis only
		// for that (and the accumulator persistence the same store
		// carries): replay makes no Rivian calls, so no rate limiter.
		if addr := strings.TrimSpace(os.Getenv("RIVOLT_REDIS_ADDR")); addr != "" && !replayRedisPrefixOK(redisKeyPrefix) {
			logger.Warn("replay: live state store DISABLED: RIVOLT_REDIS_KEY_PREFIX is empty or the default \"rivolt\", which is prod's keyspace; set a replay-only prefix (e.g. rivolt-preview)",
				"addr", addr, "prefix", redisKeyPrefix)
		} else if addr != "" {
			ropts := redisOptions(addr, os.Getenv)
			logger.Info("redis: client", append([]any{"addr", addr}, redisAuthAttrs(ropts)...)...)
			rdb := redis.NewClient(ropts)
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			err := rdb.Ping(pingCtx).Err()
			pingCancel()
			if err != nil {
				logger.Warn("replay: live state store disabled (redis unreachable)", "addr", addr, "err", err.Error())
				_ = rdb.Close()
			} else {
				sharedRedis = rdb
				redisUp.Store(true)
			}
		}
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
		var sharedLimiter *ratelimit.Retrying
		if addr := strings.TrimSpace(os.Getenv("RIVOLT_REDIS_ADDR")); addr != "" {
			ropts := redisOptions(addr, os.Getenv)
			logger.Info("redis: client", append([]any{"addr", addr}, redisAuthAttrs(ropts)...)...)
			rdb := redis.NewClient(ropts)
			rlCfg := ratelimit.DefaultConfig()
			rlCfg.KeyPrefix = redisKeyPrefix + ":rl"
			rlCfg.FallbackDivisor = replicaDivisor
			rlCfg.OnFallback = func(class ratelimit.Class) { appMetrics.RivianBudgetFallbackTotal.WithLabelValues(string(class)).Inc() }
			// Redis may not be up yet when the pod boots; the limiter keeps retrying
			// instead of staying off until the next restart.
			sharedLimiter = ratelimit.NewRetrying(ctx, rdb, rlCfg, logger, 5*time.Second, 30*time.Second, func() { redisUp.Store(true) })
			// The other Redis users (live state, plan legs) are wired once, below, over
			// this client whether or not Redis answered yet: the plan-leg cache bounds
			// and backs off its own calls, and the live-state store is gated on redisUp.
			sharedRedis = rdb
			if sharedLimiter.Ready() {
				redisUp.Store(true)
				logger.Info("ratelimit: enabled", "addr", addr, "key_prefix", rlCfg.KeyPrefix)
			}
		}

		// Set once the registry exists (below); read by each client's reauth guard.
		var accountSyncer atomic.Pointer[rivian.AccountSyncer]
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
			lc.WithResultObserver(&rivianResultMetrics{m: appMetrics})
			if sharedLimiter != nil {
				lc.WithRateLimit(&rateLimitMetrics{l: sharedLimiter, m: appMetrics})
			}
			// Persist needs_reauth transitions to Postgres so the
			// flag survives restarts. Closure captures uid so each
			// per-user client persists to its own row. The write is
			// transition-gated (false->true), so even with two
			// replicas classifying the same user at once, exactly one
			// sees transitioned=true and sends the one-shot re-auth
			// email — the user's drives have silently stopped, and
			// they won't see the in-app banner unless they sign in.
			boundUID := uid
			// A rejection of a session that another pod has since replaced says nothing
			// about the user's real session: do not raise needs_reauth for it.
			lc.WithReauthGuard(func(gctx context.Context, used string) bool {
				if s := accountSyncer.Load(); s != nil {
					return s.Superseded(gctx, boundUID, used)
				}
				return false
			})
			lc.WithReauthSink(func(sinkCtx context.Context, reason string) {
				// Empty reason is the clear signal (successful Login).
				if reason == "" {
					if err := db.SetNeedsReauth(sinkCtx, pgPool, boundUID, ""); err != nil {
						logger.Warn("clear needs_reauth", "user_id", boundUID.String(), "err", err.Error())
					}
					return
				}
				// Only if the session this client failed with is still the stored one: a
				// pod that has not yet caught up with a sign-in elsewhere must not flag the
				// user, nor send the email, right after they signed in.
				var transitioned bool
				var err error
				if accountSyncer.Load() != nil {
					loaded := rivian.SessionVersionFrom(sinkCtx)
					if loaded.IsZero() {
						// The client does not know which stored session it holds yet; the
						// next sync records it and the next rejection raises.
						appMetrics.ReauthRaiseDeferred.Inc()
						logger.Info("rivian reauth raise deferred: session version unknown", "user_id", boundUID.String())
						return
					}
					transitioned, err = db.RaiseNeedsReauthIfCurrent(sinkCtx, pgPool, boundUID, reason, loaded)
				} else {
					// No account sync (no secrets store): there is no stored session to be
					// stale against.
					transitioned, err = db.RaiseNeedsReauth(sinkCtx, pgPool, boundUID, reason)
				}
				if err != nil {
					logger.Warn("persist needs_reauth", "user_id", boundUID.String(), "reason", reason, "err", err.Error())
					return
				}
				if transitioned {
					bg.Go(func() {
						sendReauthEmail(context.Background(), mailer, pgPool, logger, boundUID, reason, os.Getenv("RIVOLT_BASE_URL"))
					})
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
			// Lazy-restore any persisted Rivian session from Postgres.
			// Without this, a user who signs in to Rivian on Pod A
			// stays "not connected" on Pod B until the next restart —
			// the boot hydrate sweep only fires once, and per-user
			// LiveClients constructed on-demand after boot would
			// otherwise start unauthenticated. Both code paths now
			// converge on the same Restore() so /api/settings/rivian
			// answers identically from any replica.
			if secretsStore != nil {
				if sess, err := secrets.LoadRivianSession(ctx, secretsStore, boundUID); err != nil {
					logger.Warn("rivian session load (lazy)",
						"user_id", boundUID.String(), "err", err.Error())
				} else if sess.UserSessionToken != "" {
					lc.Restore(sess)
				}
			}
			return lc
		}
		accountRegistry = rivian.NewLiveAccountRegistry(buildLive)
		// Keep every pod's copy of each user's Rivian account in line with the database:
		// a sign-in, sign-out or needs_reauth change made on one pod reaches the others
		// within seconds (see rivian.AccountSyncer). Without a database or a secrets store
		// there is nothing shared to follow.
		if att, ok := accountRegistry.(interface {
			AttachSyncer(*rivian.AccountSyncer)
			Raw(uuid.UUID) rivian.Account
		}); ok && pgPool != nil && secretsStore != nil {
			syncer := rivian.NewAccountSyncer(secrets.NewRivianAccountSource(pgPool, secretsStore), att.Raw, logger)
			accountSyncer.Store(syncer)
			syncer.OnChange(func(uid uuid.UUID, change rivian.SyncChange) {
				// A monitor already running here holds the old tokens (or none): stop it,
				// and on a new session start it again on the new ones.
				mr := monitorRegistry
				if mr != nil && mr.For(uid) != nil {
					mr.Stop(uid)
					if change == rivian.SyncSessionReplaced {
						mr.Start(ctx, uid)
					}
				}
				// An account deleted on another pod: after the monitor is stopped (or it writes
				// again), delete what this install holds of the user in Redis.
				if change == rivian.SyncUserRemoved {
					purgeUserCaches(uid)
				}
			})
			syncer.OnReconcile(func(r rivian.Reconcile) {
				if r.ReadFailed {
					appMetrics.RivianAccountSyncErrorsTotal.Inc()
					return // keep the last mismatch counts: nothing was compared
				}
				appMetrics.RivianAccountMismatch.WithLabelValues("session").Set(float64(r.SessionMismatch))
				appMetrics.RivianAccountMismatch.WithLabelValues("flag").Set(float64(r.FlagMismatch))
			})
			att.AttachSyncer(syncer)
			bg.Go(func() { syncer.Run(ctx, 5*time.Second, accountRegistry.Loaded) })
			logger.Info("rivian account sync: enabled")
		}
		// Stub fallback for the unauthenticated stub-of-last-resort
		// path. Per-user resolution at the handler layer always
		// picks up the registry first (see clientFor in api).
		rivianClient = rivian.NewStub()
	}

	// Server-side chargers PMTiles reader. Pulls the entire ~10 MB
	// chargers.pmtiles into memory on first request (or any restart),
	// then answers /api/maps/chargers-along queries fully in-process.
	// Replaces the SPA's per-tile fan-out, which was producing
	// hundreds of HTTP range requests per planner re-render. Lazy
	// load: the first /api/maps/chargers-along call drives Reload.
	var chargersArchive *chargers.Archive
	if base := strings.TrimSpace(os.Getenv("RIVOLT_TILES_BASE_URL")); base != "" {
		url := strings.TrimRight(base, "/") + "/chargers.pmtiles"
		chargersArchive = chargers.New(url)
		// Warm at startup, best-effort. A failed warm doesn't block
		// boot — the endpoint will retry on demand.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := chargersArchive.Reload(ctx); err != nil {
				logger.Warn("chargers archive warm-load failed (will retry on first request)", "err", err.Error())
				return
			}
			logger.Info("chargers archive loaded", "url", url, "loaded_at", chargersArchive.LoadedAt())
		}()
		// The archive is rebuilt out of band (a monthly job rewrites the file on
		// the tiles volume), so look for a new one every few hours: a conditional
		// GET, which costs nothing while the file is unchanged.
		go func() {
			t := time.NewTicker(6 * time.Hour)
			defer t.Stop()
			for range t.C {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				changed, err := chargersArchive.Refresh(ctx)
				cancel()
				switch {
				case err != nil:
					logger.Warn("chargers archive refresh failed (keeping the loaded copy)", "err", err.Error())
				case changed:
					logger.Info("chargers archive refreshed", "url", url, "loaded_at", chargersArchive.LoadedAt())
				}
			}
		}()
	}
	// The replay planner's route and charger sources. A long multi-stop route
	// takes Valhalla longer than the recorder's gap-fill budget, hence its own
	// client; without Valhalla the synthetic planner reports it cannot route.
	if tripSynth != nil {
		if pv := maps.NewValhalla(os.Getenv("RIVOLT_VALHALLA_BASE_URL")); pv != nil {
			pv.HTTP = &http.Client{Timeout: 30 * time.Second}
			tripSynth.Router = pv
		}
		if chargersArchive != nil {
			tripSynth.Chargers = syntheticplan.LazyArchive{A: chargersArchive}
		}
	}

	// Build the monitor registry. In live/mock modes the registry
	// is the only thing that owns *StateMonitor instances. Stub
	// mode skips it (no recorder, no WS).
	// Not on the admin deployment: no monitors means no recorder, leases,
	// receipts or OTA sync (all hang off the registry).
	if accountRegistry != nil && os.Getenv("RIVIAN_CLIENT") != "stub" && !adminOnlyMode {
		monitorRegistry = rivian.NewMonitorRegistry(
			pgPool, accountRegistry,
			drivesFactory, chargesFactory, samplesFactory, settingsFactory,
			logger,
		)
		monitorRegistry.SetParent(ctx)
		// Sleep gate: vehicles listed in the sleep_safe_vehicles flag
		// have their non-vehicleState subscriptions parked while asleep;
		// every vehicle's sleeps are timed into the sleeps metric.
		if flagsStore != nil {
			monitorRegistry.SetSleepSafe(func(vehicleID string) bool {
				return flagsStore.SleepSafe().Covers(vehicleID)
			})
			// Wake-safety switches (drain experiment D2), all off unless
			// the wake_safety flag selects the vehicle.
			monitorRegistry.SetWakeSafety(func(vehicleID string) rivian.WakeSafety {
				w := flagsStore.WakeSafety()
				return rivian.WakeSafety{
					CaptureOff:     w.CaptureOff.Covers(vehicleID),
					RestQuiet:      w.RestQuiet.Covers(vehicleID),
					HoldAsleep:     w.HoldAsleep.Covers(vehicleID),
					HoldCheckEvery: w.CheckEvery(),
					HoldMax:        w.MaxHold(),
				}
			})
		}
		monitorRegistry.SetSleepObserver(&sleepMetrics{m: appMetrics})
		// Price DC fast charges Rivian didn't bill at the rate of the
		// station's network (nearest NREL DCFC within dcfcMatchRadiusM),
		// honouring the user's memberships; unmatched → the table's
		// "Unknown DCFC" rate. Without it they'd land on the home rate.
		monitorRegistry.SetDCFCPriceLookupFactory(func(uid uuid.UUID) rivian.DCFCPriceLookup {
			return newDCFCPricer(chargersArchive, settingsFactory.For(uid))
		})
		// Where dcfc_auto_network covers the user, detect the network
		// with a confidence and a verify state instead (contract
		// dcfc-auto-network v1); elsewhere the pricer above stands.
		monitorRegistry.SetDCFCDetectorFactory(func(uid uuid.UUID) rivian.DCFCDetector {
			return newDCFCDetector(chargersArchive, settingsFactory.For(uid), chargesFactory.For(uid), func() bool {
				return flagsStore != nil && flagsStore.DCFCAutoNetwork().Enabled(uid.String())
			})
		})
		// A fast charge the user has no rate for is priced at a typical
		// public price while it is recorded (cost_source observed_estimate);
		// `rivolt reprice` never installs this.
		monitorRegistry.SetDCFCEstimatorFactory(func(uid uuid.UUID) rivian.DCFCEstimator {
			return newDCFCEstimator(chargersArchive, settingsFactory.For(uid))
		})
		// Live-session persistence: when Redis is wired, install a
		// per-user LiveStateStore factory so each StateMonitor can
		// rehydrate its in-flight drive/charge accumulators across
		// pod restarts and lease handoffs. Without this, a deploy or
		// a peer pod taking over a lease mid-drive fragments the
		// trip into a fresh drive row at the handover (see incident
		// on 2026-05-07).
		if sharedRedis != nil {
			monitorRegistry.SetLiveStateStoreFactory(func(uid uuid.UUID) rivian.LiveStateStore {
				return rivian.NewGatedLiveStateStore(rivian.NewRedisLiveStateStore(sharedRedis, uid.String(), redisKeyPrefix), redisUp.Load)
			})
			logger.Info("live state store: enabled (redis)")
		}
		// Cross-pod session rehydrate: when the lease coordinator hands
		// this pod a vehicle whose owner signed in on a peer pod, the
		// cached client here predates their session and the monitor
		// would wait on AuthReady forever (recording nothing). Load the
		// persisted session from user_secrets and Restore it into the
		// shared per-user client so the monitor can subscribe without a
		// pod restart.
		if secretsStore != nil && accountRegistry != nil {
			monitorRegistry.SetSessionRehydrator(func(ctx context.Context, uid uuid.UUID) bool {
				sess, err := secrets.LoadRivianSession(ctx, secretsStore, uid)
				if err != nil || sess.UserSessionToken == "" {
					return false
				}
				a := accountRegistry.For(uid)
				if a == nil {
					return false
				}
				a.Restore(sess)
				return a.Authenticated()
			})
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
					// A DC fast charge auto-priced under dcfc_auto_network
					// gets the confirm-or-change prompt instead of the
					// generic "charging complete" push (one push per
					// charge; rate limited per user).
					if flagsStore != nil && flagsStore.DCFCAutoNetwork().Enabled(uid.String()) {
						if e, ok := networkConfirmFor(c); ok {
							pending := 0
							if cs := chargesFactory.For(uid); cs != nil {
								pctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
								pending, _, _ = cs.NetworkToConfirm(pctx)
								cancel()
							}
							pushSvc.NotifyNetworkConfirm(userStore, uid.String(), e, pending)
							return
						}
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
		// The charging Live Activity (RIVOLT_APNS_LIVE): while a truck with a running
		// activity charges, send its figures every few minutes; when the charge
		// closes, send the end. Does nothing until an iPhone has turned the switch
		// on and started an activity.
		if pushSvc != nil && pushSvc.LiveEnabled() && pushFactory != nil && pgPool != nil {
			liveSrc := push.LiveSourceFunc(func(uid uuid.UUID, rivianVehicleID string) (float64, float64, float64, bool) {
				m := monitorRegistry.For(uid)
				if m == nil {
					return 0, 0, 0, false
				}
				st, at := m.Latest(rivianVehicleID)
				if st == nil || time.Since(at) > 10*time.Minute || !rivian.IsChargingState(st.ChargerState) {
					return 0, 0, 0, false
				}
				// The truck's own time left when it is valid, else ours (the same rule as Now; see rivian.PickChargeETA).
				mins, _ := rivian.ChargeETAFromState(st)
				return st.BatteryLevelPct, st.ChargerPowerKW, mins, true
			})
			go pushSvc.RunLive(ctx, pgPool, pushFactory, liveSrc, push.LiveInterval)
			monitorRegistry.AddChargeCloseHookFactory(func(uid uuid.UUID) rivian.ChargeCloseHook {
				return func(hctx context.Context, c charges.Charge) {
					ectx, cancel := context.WithTimeout(context.WithoutCancel(hctx), 30*time.Second)
					defer cancel()
					pushSvc.EndLiveForCharge(ectx, pushFactory, uid, c.VehicleID, c.EndSoCPct)
				}
			})
			logger.Info("live activities: charging updates enabled")
		}
		// Write-through: whenever Rivian's vehicleState.batteryCapacity
		// changes for a vehicle, mirror the new kWh into vehicles.pack_kwh
		// so the percentage-of-nameplate display and the SoC × pack
		// derivation pick up the truck's self-report instead of the
		// trim-table guess. Runs off the recorder goroutine on a
		// short timeout so DB hiccups can't stall the WS path.
		if pgPool != nil {
			monitorRegistry.SetBatteryCapacityHookFactory(func(uid uuid.UUID) func(vehicleID string, kwh float64) {
				return func(vehicleID string, kwh float64) {
					bg.Go(func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						_, err := pgPool.ExecContext(ctx, `
							UPDATE vehicles
							SET pack_kwh = $3, updated_at = NOW()
							WHERE user_id = $1
							  AND rivian_vehicle_id = $2
							  AND (pack_kwh IS NULL OR pack_kwh <> $3)
						`, uid, vehicleID, kwh)
						if err != nil {
							logger.Warn("persist battery capacity",
								"user_id", uid.String(),
								"vehicle_id", vehicleID,
								"kwh", kwh,
								"err", err.Error())
						}
					})
				}
			})
			logger.Info("battery capacity persistence hook enabled")
		}
		// Persist vehicle metadata (trim, hero image) from the monitor's own
		// RefreshVehicleInfo, which already fetched it: the vehicles.trim and
		// image_url columns were empty for every vehicle but the one whose
		// owner had opened /api/vehicles. COALESCE in the upsert means a
		// blank never overwrites a stored value.
		if pgPool != nil {
			// OTA release details (version, release-notes link): once a day
			// per vehicle and when a monitor connects (after the rows above
			// exist), stored in vehicles.metadata and served by /api/vehicles.
			otaSyncer := otadetails.New(pgPool, accountRegistry,
				func(ctx context.Context) ([]uuid.UUID, error) { return db.ListUsersWithRivianSession(ctx, pgPool) }, logger)
			if pushSvc != nil && pushFactory != nil {
				otaSyncer.SetNotifier(func(u otadetails.Update) {
					pushSvc.NotifySoftwareUpdate(pushFactory.For(u.UserID), u.Vehicle, u.Version, u.Installed)
				})
			}
			go otaSyncer.Run(ctx)
			monitorRegistry.SetVehicleInfoHookFactory(func(uid uuid.UUID) func([]rivian.Vehicle) {
				return func(vs []rivian.Vehicle) {
					bg.Go(func() {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						for _, v := range vs {
							if v.ID == "" {
								continue
							}
							if err := db.UpsertVehicleFromSync(ctx, pgPool, uid, db.VehicleSync{
								RivianID: v.ID, VIN: v.VIN, Name: v.Name, Model: v.Model, ModelYear: v.ModelYear,
								Trim: v.TrimID, PackKWh: v.PackKWh, ImageURL: v.ImageURL,
							}); err != nil {
								logger.Warn("persist vehicle info", "user_id", uid.String(), "rivian_vehicle_id", v.ID, "err", err.Error())
							}
						}
						otaSyncer.OnConnect(uid)
					})
				}
			})
		}
		// Rivian charging receipts: what Rivian billed per session, stored
		// next to our estimate (charges.receipt_*). Gated per user by the
		// rivian_receipts flag; one summaries call per user a day plus one
		// after a charge closes, details paced (internal/receipts).
		if pgPool != nil && flagsStore != nil {
			receiptSyncer := receipts.NewSyncer(
				receipts.NewStore(pgPool), accountRegistry,
				// Operator flag AND the user's own opt-in (off by default).
				func(uid uuid.UUID) bool {
					return flagsStore.RivianReceipts().Enabled(uid.String()) &&
						settingsFactory.RivianReceiptsOptIn(context.Background(), uid)
				},
				func(ctx context.Context) ([]uuid.UUID, error) { return db.ListUsersWithRivianSession(ctx, pgPool) },
				logger,
			)
			monitorRegistry.AddChargeCloseHookFactory(func(uid uuid.UUID) rivian.ChargeCloseHook {
				return func(_ context.Context, _ charges.Charge) { receiptSyncer.OnChargeClose(uid) }
			})
			go receiptSyncer.Run(ctx)
			logger.Info("rivian receipts sync enabled (per-user flag rivian_receipts)")
		}
		// Milestones (internal/milestones): after a drive or charge closes, notice a
		// round number passed (1,000 mi, 1 MWh at home, 100 charges, lease halfway, a
		// personal-best efficiency). A card always; a push only to devices that turned
		// milestones on, through engage and only while two budget slots stay free for
		// the weekly recap. Nothing is announced until the one-off backfill has marked
		// the milestones already passed.
		if pgPool != nil && !adminOnlyMode && !demo.OnlyEnabled() {
			msStore := milestones.NewStore(pgPool)
			msEngage := &engage.Engage{Store: engage.NewStore(pgPool), Observe: func(kind, outcome string) {
				appMetrics.EngagementSendsTotal.WithLabelValues(kind, outcome).Inc()
			}}
			checker := &milestones.Checker{
				Store: msStore, Input: api.MilestoneLedgerInput(pgPool, settingsFactory), Lease: api.MilestoneLease(pgPool), Logger: logger,
			}
			if pushSvc != nil && pushFactory != nil {
				checker.Push = func(ctx context.Context, uid, vehicle uuid.UUID, r milestones.Row) {
					store := pushFactory.For(uid)
					if store == nil {
						return
					}
					used, err := msEngage.Store.BudgetUsed(ctx, uid, time.Now().Add(-engage.BudgetWindow))
					if err != nil || !milestones.PushAllowed(used) {
						appMetrics.EngagementSendsTotal.WithLabelValues("milestone", "skipped_reserve").Inc()
						return
					}
					title, body := milestones.Text(r.Kind, r.Detail)
					_, _ = msEngage.Send(ctx, engage.Msg{
						User: uid, Kind: "milestone", Key: "milestone:" + r.Key + ":" + vehicle.String(), Channel: engage.Push,
						Toggle: func(ctx context.Context) (bool, error) {
							subs, err := store.List(ctx)
							for _, sub := range subs {
								if sub.OnMilestones {
									return true, err
								}
							}
							return false, err
						},
					}, func(ctx context.Context) error {
						_, err := pushSvc.DeliverMilestone(ctx, store, title, body)
						return err
					})
				}
			}
			monitorRegistry.AddDriveCloseHookFactory(func(uid uuid.UUID) rivian.DriveCloseHook {
				return func(ctx context.Context, d drives.Drive) {
					ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
					defer cancel()
					checker.Check(ctx, uid, d.VehicleID, d.ID)
				}
			})
			monitorRegistry.AddChargeCloseHookFactory(func(uid uuid.UUID) rivian.ChargeCloseHook {
				return func(ctx context.Context, c charges.Charge) {
					ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
					defer cancel()
					checker.Check(ctx, uid, c.VehicleID, "")
				}
			})
			go checker.Run(ctx)
			// Trip recap (internal/api/trip_recap.go): after a drive closes, a push to the user who
			// turned "Trip recap" on, when the drive finished a saved trip. Off by default, through engage.
			if pushSvc != nil && pushFactory != nil {
				recap := api.NewTripRecap(pgPool, trips.NewFactory(pgPool), settingsFactory, msEngage,
					func(ctx context.Context, uid uuid.UUID, p push.Payload) error {
						store := pushFactory.For(uid)
						if store == nil {
							return errors.New("push: no store")
						}
						_, err := pushSvc.DeliverTripRecap(ctx, store, p)
						return err
					}, logger)
				monitorRegistry.AddDriveCloseHookFactory(func(uid uuid.UUID) rivian.DriveCloseHook {
					return func(ctx context.Context, _ drives.Drive) {
						ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
						defer cancel()
						recap.OnDriveClose(ctx, uid)
					}
				})
			}
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

	// Public demo (RIVOLT_DEMO=true on a replay install; preview only):
	// seed the demo user, settings, replay session and older charges
	// before the hydrate sweep below connects it. Idempotent.
	if demo.Enabled() && pgPool != nil && secretsStore != nil {
		if err := demo.Seed(ctx, demo.Deps{
			DB: pgPool, Settings: settingsFactory, Charges: chargesFactory, Drives: drivesFactory, Secrets: secretsStore,
		}); err != nil {
			logger.Error("demo seed failed", "err", err.Error())
		} else {
			logger.Info("demo account seeded", "vehicle", demo.VehicleID())
		}
		// The replay backfill records sessions after boot, so the
		// hand-priced hotel L2 gets its user price on a timer.
		go demo.RunPricing(ctx, pgPool, chargesFactory.For(demo.UserID()), 15*time.Minute, func(msg string, args ...any) { logger.Info(msg, args...) })
		// Sample weather for the demo drives, so cold-range has stored temperatures.
		go demo.RunWeather(ctx, pgPool, 15*time.Minute, func(msg string, args ...any) { logger.Info(msg, args...) })
		// The sample owner's saved Las Vegas plans follow the replay onto
		// each loop's trip, for the demo's Plan vs Actual.
		go demo.RunPlans(ctx, pgPool, settingsFactory, 15*time.Minute, func(msg string, args ...any) { logger.Info(msg, args...) })
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
				acct := accountRegistry.For(uid)
				acct.Restore(sess)
				// A replay account ignores sessions it didn't issue (e.g.
				// a real Rivian session from before the switch); skip it.
				if _, live := acct.(*rivian.LiveClient); !live && !acct.Authenticated() {
					continue
				}
				if monitorRegistry != nil {
					monitorRegistry.Start(ctx, uid)
				}
				restored++
				logger.Info("rivian session restored",
					"user_id", uid.String())
			}
			logger.Info("rivian sessions hydrated", "mode", os.Getenv("RIVIAN_CLIENT"), "users_hydrated", restored)
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
	if pgPool != nil && !adminOnlyMode {
		partitionJanitor := samples.NewPartitionJanitor(pgPool)
		go partitionJanitor.Run(ctx)
	}

	// Fleet parked-drain gauges (D1): computed from vehicle_state on a
	// ticker, served from a snapshot on scrape. Reads only our own
	// database, never Rivian, so it can't wake a car.
	// RIVOLT_DRAIN_METRICS_INTERVAL=off disables it.
	if pgPool != nil && !adminOnlyMode {
		iv, on, ierr := drain.ParseInterval(os.Getenv("RIVOLT_DRAIN_METRICS_INTERVAL"))
		if ierr != nil {
			logger.Warn("drain metrics: bad RIVOLT_DRAIN_METRICS_INTERVAL, using default", "err", ierr.Error())
		}
		if on {
			dc := drain.NewCollector()
			appMetrics.Registry().MustRegister(dc)
			go dc.Run(ctx, pgPool, iv, logger)
			logger.Info("drain metrics enabled", "interval", iv.String())
		}
	}

	// Derive pack-health samples from existing charges on a slow
	// ticker so the "Pack health" UI has data without a per-charge
	// recorder hook. Idempotent (Upsert replaces on conflict);
	// startup run primes the table, hourly run picks up new
	// charges. Skips entirely when no DB is wired (dev mode).
	if pgPool != nil && !adminOnlyMode {
		ph := packhealth.NewStore(pgPool)
		go func() {
			run := func() {
				bctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				n, err := ph.BackfillAll(bctx)
				if err != nil {
					logger.Warn("packhealth backfill failed", "err", err.Error())
					return
				}
				if n > 0 {
					logger.Info("packhealth backfill", "samples", n)
				}
			}
			run()
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					run()
				}
			}
		}()
	}

	// Engagement email sweep. Two re-engagement loops that would
	// otherwise each need their own ticker share one slow tick:
	//
	//   - Re-nudge users stuck needing Rivian re-auth. The reauth sink
	//     emails once on the rising edge, but users who flipped before
	//     that feature shipped never got one and a single email is easy
	//     to miss. Re-sends to anyone still stuck, throttled per-user by
	//     needs_reauth_notified_at.
	//   - Finish-setup nudge: users who never connected a Rivian account
	//     get up to two emails (a day after signup, then a week later)
	//     pointing them back to the connect step.
	//
	// Both read a bounded "who is due?" set and Claim-gate each send, so
	// every replica can run this without double-emailing.
	//
	// RIVOLT_ENGAGEMENT_EMAILS=false opts an install out entirely -
	// meant for staging/preview environments that share a mailer and a
	// copy of real user data, where a nudge pointing at the wrong
	// domain (or a duplicate of prod's) is worse than none.
	// onboardSigner is set once the cookie secret is known (below); the
	// onboarding emails are not sent until then.
	var onboardSigner atomic.Pointer[statement.Signer]
	if pgPool != nil && mailer != nil && !adminOnlyMode && os.Getenv("RIVOLT_ENGAGEMENT_EMAILS") != "false" {
		nudgeBaseURL := os.Getenv("RIVOLT_BASE_URL")
		const reauthNudgeCooldown = 7 * 24 * time.Hour
		go func() {
			runReauth := func(sctx context.Context) {
				due, err := db.ListUsersDueForReauthNudge(sctx, pgPool, reauthNudgeCooldown)
				if err != nil {
					logger.Warn("reauth nudge sweep: list failed", "err", err.Error())
					return
				}
				sent := 0
				for _, n := range due {
					claimed, cerr := db.ClaimReauthNudge(sctx, pgPool, n.UserID, reauthNudgeCooldown)
					if cerr != nil {
						logger.Warn("reauth nudge sweep: claim failed",
							"user_id", n.UserID.String(), "err", cerr.Error())
						continue
					}
					if !claimed {
						continue // another replica took this one
					}
					sendReauthEmail(sctx, mailer, pgPool, logger, n.UserID, n.Reason, nudgeBaseURL)
					sent++
				}
				if sent > 0 {
					logger.Info("reauth nudge sweep", "nudged", sent)
				}
			}
			// Welcome email and setup reminders (internal/onboardmail): at
			// most three per user, verified addresses only, one-click
			// unsubscribe. Needs the signer (no way to opt out, no mail).
			onboard := &onboardmail.Job{
				DB: pgPool, Mail: mailer, Signer: onboardSigner.Load, BaseURL: nudgeBaseURL, Logger: logger,
				OnSent: func(kind string, attempt int, uid uuid.UUID) {
					appMetrics.FunnelEventsTotal.WithLabelValues(kind + "_sent").Inc()
					logger.Info("funnel", "event", kind+"_sent", "user_id", uid.String(), "attempt", attempt)
				},
			}
			runSetup := func(sctx context.Context) {
				if w, n := onboard.Sweep(sctx); w+n > 0 {
					logger.Info("onboarding email sweep", "welcome", w, "setup_nudges", n)
				}
			}
			run := func() {
				sctx, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				runReauth(sctx)
				runSetup(sctx)
			}
			// Brief startup delay so the sweep doesn't race boot hydrate
			// / DB warmup, then a slow tick.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Minute):
			}
			run()
			t := time.NewTicker(6 * time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					run()
				}
			}
		}()
	}

	// Rivian session expiry (internal/rivianexpiry): Rivian ends a session 180 days after
	// sign-in and offers no way to extend it, so count the sessions near their end every hour
	// (metrics) and email each user once at 7 days and once at 1 day left. Every replica may
	// run it: the notices are claimed in engagement_sends. RIVOLT_ENGAGEMENT_EMAILS=false
	// (preview) keeps the counts and sends nothing.
	rivian.ConfiguredSessionLifetime = rivian.SessionLifetimeFromEnv(os.Getenv)
	rivianExpiryEmails := false
	if pgPool != nil && secretsStore != nil && !adminOnlyMode {
		expiry := &rivianexpiry.Job{
			Sessions: secrets.NewRivianSessionLister(pgPool, secretsStore).WithLog(logger),
			Claims:   engage.NewStore(pgPool),
			Email:    func(c context.Context, uid uuid.UUID) (string, error) { return db.LookupUserEmail(c, pgPool, uid) },
			BaseURL:  os.Getenv("RIVOLT_BASE_URL"),
			Signer:   onboardSigner.Load,
			Skip:     func(uid uuid.UUID) bool { return demo.Enabled() && uid == demo.UserID() },
			Lifetime: rivian.ConfiguredSessionLifetime,
			Log:      logger,
			OnCounts: func(c rivianexpiry.Counts) {
				appMetrics.RivianSessionsExpiring.WithLabelValues("14d").Set(float64(c.Within14))
				appMetrics.RivianSessionsExpiring.WithLabelValues("7d").Set(float64(c.Within7))
				appMetrics.RivianSessionsExpiring.WithLabelValues("1d").Set(float64(c.Within1))
				appMetrics.RivianSessionsExpired.Set(float64(c.Expired))
			},
			OnNotice: func(days int, outcome string) {
				appMetrics.RivianExpiryNoticesTotal.WithLabelValues(strconv.Itoa(days), outcome).Inc()
			},
		}
		// The emails are off unless RIVOLT_RIVIAN_EXPIRY_EMAILS=true (the wording is the owner's to
		// approve, N49): without it the sweep still counts and the status fields and banners work,
		// but nothing is claimed or sent. RIVOLT_ENGAGEMENT_EMAILS=false (preview) also keeps it off.
		if mailer != nil && os.Getenv("RIVOLT_RIVIAN_EXPIRY_EMAILS") == "true" && os.Getenv("RIVOLT_ENGAGEMENT_EMAILS") != "false" {
			expiry.Mail = mailer
		}
		rivianExpiryEmails = expiry.Mail != nil
		logger.Info("rivian expiry emails", "enabled", rivianExpiryEmails)
		bg.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Minute):
			}
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				expiry.Sweep(sctx)
				cancel()
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		})
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
		// Heal a lease held without a subscription (an exited run, a
		// monitor that never started): reconcile re-subscribes it and
		// counts the episode.
		coord.SetGroupObserver(
			func(n int) { appMetrics.LeaseGroupsOwned.Set(float64(n)) },
			func(source string, n int) {
				appMetrics.LeaseFenceWouldReject.WithLabelValues("db", source).Add(float64(n))
			},
		)
		coord.SetStreamingCheck(monitorRegistry.Streaming)
		coord.SetUnstreamedObserver(func(string) { appMetrics.LeaseUnstreamed.Inc() })
		// Authoritative set for lease ownership: vehicles whose owner has
		// a stored Rivian session. A lease outside this set is reaped and
		// unsubscribed. This covers both account deletion (vehicles row
		// gone, and subscription_leases has no FK to cascade it) and
		// disconnect (logout deletes the session row) - in the latter
		// case every replica drops the lease within one reconcile instead
		// of the lease-owning pod streaming on from memory until its next
		// restart. Gated on the same 'rivian.session' predicate as the
		// boot hydrate so what gets a monitor and what stays leased agree.
		coord.SetAuthoritative(func(qctx context.Context) ([]string, error) {
			return db.ListSubscribableVehicleIDs(qctx, pgPool)
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
	// Header SSO believes the user header from any peer in this list. A
	// network-wide entry (the pod network, a /16) trusts the ingress and every
	// neighbour, so anyone who reaches the app through them can sign in as any
	// user by sending the header. With OIDC sign-in configured (a public
	// site) that is a refusal to start; otherwise a loud error at every boot.
	oidcConfigured := strings.TrimSpace(os.Getenv("RIVOLT_OIDC_PROVIDERS")) != ""
	if broad, cerr := auth.CheckTrustedProxies(trustedNets, oidcConfigured); cerr != nil {
		logger.Error("refusing to start: "+cerr.Error(), "cidrs", strings.Join(broad, ","))
		os.Exit(1)
	} else if len(broad) > 0 {
		logger.Error("RIVOLT_TRUSTED_PROXY_CIDR is too broad: it trusts the user header from every address in "+strings.Join(broad, ", ")+
			" (the pod network or a /16 or wider). Anyone who can reach this app through those peers can sign in as any user. "+
			"Trust only the exact address of an authenticating proxy (a /32), or unset it.",
			"cidrs", strings.Join(broad, ","))
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
				logger.Error("bypass user ensure", "user_hash", logging.IDHash(bypassUser), "err", logging.ScrubErr(err))
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

	if err := checkKratosCookieConfig(os.Getenv); err != nil {
		logger.Error("kratos cookie config", "err", err.Error())
		os.Exit(1)
	}

	authSvc, err := auth.New(auth.Config{
		// A parent-domain Kratos cookie reaches sibling hosts (preview,
		// demo). RIVOLT_KRATOS_COOKIE_HOST names the one host where Kratos
		// cookies are legitimately issued (rivolt.dev on prod AND preview:
		// not each install's own host, or preview would accept prod's
		// cookie). Unset leaves the guard off.
		KratosHost:        os.Getenv("RIVOLT_KRATOS_COOKIE_HOST"),
		CookieSecret:      cookieSecret,
		SecureCookie:      secureCookie,
		TrustedProxyCIDRs: trustedNets,
		OnAuth:            func(method string) { appMetrics.AuthRequestsTotal.WithLabelValues(method).Inc() },
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
		//
		// OFF unless RIVOLT_ACCEPT_KRATOS_COOKIE=true. Nothing in the app starts a Kratos browser flow, so no legitimate user holds this
		// cookie; its only effect was that anyone with a password could mint one through the public Kratos route and be signed in for
		// 30 days with no Rivolt session (found Oct 25, ended on logout in #1117). A request carrying only the cookie is anonymous.
		KratosResolver: kratosCookieResolver(os.Getenv, func(ctx context.Context, cookieHeader string) (uuid.UUID, bool, error) {
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
			// One switch for every first-party path (the Hydra consent
			// claim uses the same one): first-party emails count as
			// verified unless RIVOLT_TRUST_FIRST_PARTY_EMAILS=false,
			// in which case Kratos's own verification state decides.
			firstPartyVerified := identity.FirstPartyEmailVerified(*id)
			if pgPool != nil {
				// RIVOLT_SIGNUP_DISABLED: a Kratos session for an
				// identity with no Rivolt account doesn't create one.
				if signupgate.Disabled() {
					ok, err := signupgate.Allow(ctx, pgPool, email, email, firstPartyVerified, id.Traits.DisplayName)
					if err != nil {
						return uuid.Nil, false, err
					}
					if !ok {
						return uuid.Nil, false, auth.ErrSignupClosed
					}
				}
				uid, err := db.EnsureUserVerified(db.WithSignupMethod(ctx, "email"), pgPool, email, email, id.Traits.DisplayName, firstPartyVerified)
				if err != nil {
					return uuid.Nil, false, err
				}
				return uid, true, nil
			}
			return db.UserIDFor(email), true, nil
		}),
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
	var sessionStore *sessions.Store
	if pgPool != nil {
		sStore, serr := sessions.New(pgPool, cookieSecret)
		if serr != nil {
			logger.Error("sessions init", "err", serr.Error())
			os.Exit(1)
		}
		sessionStore = sStore
		authSvc.WithSessionStore(sessions.NewAuthAdapter(sStore))
		// The iPhone app's bearer tokens, everywhere but the admin
		// deployment (admin is a browser session behind Access).
		authSvc.AcceptBearer(!adminOnlyMode)
		logger.Info("sessions store ready — cookies are opaque, revocable")
	}

	// OIDC: third issuer alongside static creds + trusted-proxy
	// header. Disabled when RIVOLT_OIDC_PROVIDERS is empty so a
	// fresh install ships zero behaviour change. When enabled it
	// requires pgPool (we need EnsureUserFull and a sessions
	// store) — emit a clear error rather than silently dropping.
	var oidcSvc *oidc.Service
	var appleNotifier http.Handler // an interface, so it stays nil unless the Apple provider is set up
	// Passkeys (WebAuthn) for the email accounts: only with RIVOLT_WEBAUTHN_RP_ID and _ORIGINS set, a database, and not on the
	// admin or demo-only deployments. The pointer stays nil otherwise, so no passkey route exists.
	var passkeySvc *passkeys.Service
	// The passkey tables on the system pool, built whenever there is a database, WebAuthn configured or not: "Secure my account"
	// and support's reset delete credentials through it. A deployment with passkeys off (the admin site, prod before the enable)
	// can still hold rows, and must not skip their deletion.
	var passkeyStore passkeys.Store
	if pgPool != nil {
		passkeyStore = passkeys.NewPGStore(pgPool)
	}
	if cfg := passkeys.ConfigFromEnv(os.Getenv); cfg.Enabled() && pgPool != nil && !adminOnlyMode && !demo.OnlyEnabled() {
		svc, perr := passkeys.New(cfg, passkeyStore)
		if perr != nil {
			logger.Error("passkeys init", "err", perr.Error())
			os.Exit(1)
		}
		passkeySvc = svc
		logger.Info("passkeys enabled", "rp_id", cfg.RPID, "origins", len(cfg.Origins))
	}
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
			UserExists: func(ctx context.Context, username string) (bool, error) {
				return db.UserExistsByUsername(ctx, pgPool, username)
			},
			EnsureUser: func(ctx context.Context, username, email, displayName string, emailVerified bool) (uuid.UUID, error) {
				uid, err := db.EnsureUserVerified(db.WithSignupMethod(ctx, "oidc"), pgPool, username, email, displayName, emailVerified)
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
			CapGuard:     signupgate.NewCapGuard(pgPool, func() int { return flagsStore.SignupCap().Limit }),
			AppleLinks:   appleLinks{appleid.New(pgPool, secretsStore)},
		})
		if oerr != nil {
			logger.Error("oidc init", "err", oerr.Error())
			os.Exit(1)
		}
		oidcSvc = svc
		// Apple's server-to-server notifications, when the Apple provider is configured.
		for _, p := range provs {
			if p.Kind == "apple" && sessionStore != nil {
				issuer := p.IssuerURL
				if issuer == "" {
					issuer = "https://appleid.apple.com"
				}
				appleNotifier = &appleid.Notifier{
					Verify:   appleid.NewVerifier(ctx, issuer, p.ClientID),
					Store:    appleid.New(pgPool, secretsStore),
					Sessions: sessionStore,
					Mail:     mailer,
					Logger:   logger,
				}
			}
		}
		names := make([]string, 0, len(provs))
		for _, p := range provs {
			names = append(names, p.Name)
		}
		logger.Info("oidc enabled", "providers", names)
	}

	// The iPhone app's sign-in (iOS design §2): only with OIDC and the
	// sessions store, never on the admin or demo-only deployment, and only
	// when RIVOLT_APP_CALLBACK_URL names the app's fixed callback. There is
	// no default: rivolt.dev must use its https Universal Link (with the
	// published apple-app-site-association), never the rivolt:// scheme,
	// which any app on the phone can claim (DA on #769); a self-hosted
	// server opts in with rivolt://auth.
	// The iPhone app's associated domain: RIVOLT_IOS_APP_IDS, comma-separated
	// "TEAMID.bundle.id" (rivolt.dev: CK383X39S5.dev.rivolt.app), publishes
	// /.well-known/apple-app-site-association and the /app/callback fallback.
	// Unset: neither is served. Set it together with the https
	// RIVOLT_APP_CALLBACK_URL once the app-login enable gate is met.
	var iosAppIDs []string
	for _, id := range strings.Split(os.Getenv("RIVOLT_IOS_APP_IDS"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !api.ValidAppID(id) {
			logger.Error("bad RIVOLT_IOS_APP_IDS entry: want TEAMID.bundle.id", "value", id)
			os.Exit(1)
		}
		iosAppIDs = append(iosAppIDs, id)
	}

	var appAuth *api.AppAuth
	callback := strings.TrimSpace(os.Getenv("RIVOLT_APP_CALLBACK_URL"))
	if callback != "" && oidcSvc != nil && sessionStore != nil && !adminOnlyMode && !demo.OnlyEnabled() {
		if u, err := url.Parse(callback); err != nil || (u.Scheme != "rivolt" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
			logger.Error("bad RIVOLT_APP_CALLBACK_URL: want rivolt://… or https://… with no query", "value", callback)
			os.Exit(1)
		}
		minBuild := 0
		if v := strings.TrimSpace(os.Getenv("RIVOLT_IOS_MIN_BUILD")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				logger.Error("bad RIVOLT_IOS_MIN_BUILD", "value", v)
				os.Exit(1)
			}
			minBuild = n
		}
		aStore, err := appauth.New(pgPool, cookieSecret)
		if err != nil {
			logger.Error("app sign-in init", "err", err.Error())
			os.Exit(1)
		}
		ss := sessionStore
		appAuth = &api.AppAuth{
			Store:        aStore,
			CallbackURL:  callback,
			SecureCookie: secureCookie,
			MinIOSBuild:  minBuild,
			NewSession: func(ctx context.Context, uid uuid.UUID, r *http.Request, label string) (string, time.Time, error) {
				sess, raw, err := ss.Create(ctx, uid, api.AppSessionTTL, sessions.CreateOpts{
					UserAgent:   r.UserAgent(),
					IPAddress:   hostOnly(r.RemoteAddr), // the real client (clientip.Middleware)
					DeviceLabel: label,
					Kind:        sessions.KindApp,
				})
				return raw, sess.ExpiresAt, err
			},
		}
		logger.Info("app sign-in enabled", "callback", callback, "min_ios_build", minBuild)
	}

	if signupgate.Disabled() {
		logger.Info("signups disabled (RIVOLT_SIGNUP_DISABLED): /signup, access requests and first sign-in of unknown identities are closed; existing and admin-created users are unaffected")
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

	// RIVOLT_IMPERSONATION_DISABLED is the admin "View as user" kill
	// switch: when true the impersonation middleware ignores
	// X-Rivolt-Impersonate outright (even from a confirmed admin)
	// and /api/config tells the SPA to hide the "View as" control.
	// Defaults to enabled (false) — an operator opts out, not in.
	var impersonationDisabled bool
	if v := os.Getenv("RIVOLT_IMPERSONATION_DISABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			logger.Error("bad RIVOLT_IMPERSONATION_DISABLED", "value", v, "err", err.Error())
			os.Exit(1)
		}
		impersonationDisabled = b
	}
	// RIVOLT_ADMIN_DISABLED: this deployment serves no admin surface
	// (/admin*, /api/admin* answer 404; impersonation off; /me hides the
	// admin role). RIVOLT_ADMIN_DEBUG_PROBES: mount the raw Rivian probes
	// under /api/admin. Both default to false.
	parseFlag := func(name string) bool {
		v := os.Getenv(name)
		if v == "" {
			return false
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			logger.Error("bad "+name, "value", v, "err", err.Error())
			os.Exit(1)
		}
		return b
	}
	adminDisabled := parseFlag("RIVOLT_ADMIN_DISABLED")
	adminDebugProbes := parseFlag("RIVOLT_ADMIN_DEBUG_PROBES")
	if adminDisabled {
		logger.Info("admin surface disabled on this deployment (RIVOLT_ADMIN_DISABLED)")
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
		// Follow settings changed on another replica (recap weather, AI keys, GPS
		// thresholds), so an operator's change reaches every pod within seconds, not
		// at each pod's next restart.
		bg.Go(func() { mgr.Sync(ctx, 5*time.Second) })
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

	// Valhalla same-origin proxy. RIVOLT_VALHALLA_BASE_URL points
	// at the cluster Service serving the routing API (e.g.
	// http://valhalla.valhalla.svc.cluster.local). When set,
	// /api/maps/valhalla/* forwards to this URL and /api/config
	// advertises the path so the SPA uses it for /trace_route and
	// /route. Empty disables snap entirely — the SPA renders the
	// raw GPS chord.
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

	// Signup-request store backs the public POST /api/signup/request
	// + admin approval flow. nil-safe in api.Deps — when there is
	// no DB the entire signup surface stays unmounted.
	var signupRequestStore *signuprequests.Store
	if pgPool != nil {
		signupRequestStore = signuprequests.New(pgPool)
	}

	// Retry identity-provider deletions that failed during account
	// deletion (recorded in idp_delete_failures), with per-row backoff.
	go api.RunIdPDeleteRetrier(ctx, pgPool, userProvider, logger, 5*time.Minute)

	// Sign in with Apple: every account deletion revokes the account's Apple
	// token first, or queues the revoke (sealed) for this retrier.
	var appleRevocation *api.AppleRevocation
	if pgPool != nil {
		appleRevocation = &api.AppleRevocation{
			DB:     pgPool,
			Token:  appleid.New(pgPool, secretsStore).RefreshToken,
			Sealer: sealer,
			Log:    logger,
		}
		if oidcSvc != nil {
			appleRevocation.Revoke = oidcSvc.AppleRevoke
		}
		go api.RunAppleRevokeRetrier(ctx, appleRevocation, 5*time.Minute)
	}

	// Inactive-account purge: accounts that never connected a vehicle are
	// warned at day 90 and deleted at day 120. Off unless the
	// inactive_account_purge flag says so ({"dry_run":true} counts only); the
	// same RIVOLT_ENGAGEMENT_EMAILS=false opt-out as the nudge sweeps keeps a
	// preview that shares a copy of real users from mailing or deleting them.
	if pgPool != nil && flagsStore != nil && !adminOnlyMode && os.Getenv("RIVOLT_ENGAGEMENT_EMAILS") != "false" {
		purge := api.NewInactivePurge(pgPool, flagsStore.InactiveAccountPurge, mailer, userProvider, os.Getenv("RIVOLT_BASE_URL"), logger).
			WithAppleRevocation(appleRevocation)
		go purge.Run(ctx)
	}

	// Sample thinning: RIVOLT_SAMPLE_THIN_AFTER_DAYS (0 or unset: never;
	// at least 30) reduces old vehicle_state months to a coarser copy;
	// RIVOLT_SAMPLE_THIN_DRY_RUN=1 only logs what it would keep. A bad value
	// stops startup rather than guessing.
	sampleThinAfterDays, err := samples.ParseThinAfterDays(os.Getenv("RIVOLT_SAMPLE_THIN_AFTER_DAYS"))
	if err != nil {
		logger.Error("sample thinning", "err", err.Error())
		os.Exit(1)
	}
	// Daily retention sweeps: expired sessions (IP and User-Agent) 30 days
	// after expiry, and waitlist requests (never approved: 90 days;
	// approved: until the account exists).
	// Raw telemetry: RIVOLT_SAMPLE_RETENTION_MONTHS (0 or unset keeps it
	// forever); a bad value stops startup rather than guessing.
	sampleRetentionMonths, err := samples.ParseRetentionMonths(os.Getenv("RIVOLT_SAMPLE_RETENTION_MONTHS"))
	if err != nil {
		logger.Error("sample retention", "err", err.Error())
		os.Exit(1)
	}
	// Telemetry archive (N19): on only when RIVOLT_ARCHIVE_ENV is set; then a
	// bad config or a missing KEK stops startup rather than skipping it.
	archiveCfg, archiveOn, err := archive.ConfigFromEnv(os.Getenv)
	if err != nil {
		logger.Error("telemetry archive", "err", err.Error())
		os.Exit(1)
	}
	if pgPool != nil && !adminOnlyMode {
		jobs := []retention.Job{
			{Name: "admin_audit", Run: adminaudit.New(pgPool).Purge},
			{Name: "sessions", Run: func(ctx context.Context) (int64, error) {
				if sessionStore == nil {
					return 0, nil
				}
				return sessionStore.PurgeExpired(ctx, 30*24*time.Hour)
			}},
		}
		// Passkey ceremonies that were begun and never finished (a challenge lives five minutes).
		if passkeySvc != nil {
			jobs = append(jobs, retention.Job{Name: "webauthn_ceremonies", Run: func(ctx context.Context) (int64, error) {
				return passkeySvc.Store().PurgeCeremonies(ctx, time.Now())
			}})
		}
		// Apple notification replay guard (hashes only; the freshness window is an hour).
		jobs = append(jobs, retention.Job{Name: "apple_notification_seen", Run: appleid.New(pgPool, nil).PurgeSeen})
		// Expired iPhone-app sign-in attempts (minutes-long; kept a day).
		if appAuth != nil {
			jobs = append(jobs, retention.Job{Name: "app_auth_requests", Run: appAuth.Store.Purge})
		}
		// Backstop for the receipts opt-out: delete the receipts of any user
		// who is not opted in, in case a sync pass saved rows after they
		// opted out.
		jobs = append(jobs, retention.Job{Name: "rivian_receipts_not_opted_in", Run: receipts.NewStore(pgPool).PurgeNotOptedIn})
		// The send log of non-transactional messages (internal/engage): rows
		// past 400 days go, and claims stuck in 'sending' are reported.
		engageMaint := &engage.Engage{Store: engage.NewStore(pgPool)}
		jobs = append(jobs, retention.Job{Name: "engagement_sends", Run: func(ctx context.Context) (int64, error) {
			purged, stuck, err := engageMaint.Maintain(ctx)
			if err == nil {
				appMetrics.EngagementStuckSending.Set(float64(stuck))
			}
			return purged, err
		}})
		if signupRequestStore != nil {
			jobs = append(jobs, retention.Job{Name: "signup_requests", Run: signupRequestStore.PurgeStale})
		}
		// The telemetry archive runs before the sample jobs: thinning and
		// retention only touch months it has verified.
		if archiveOn {
			arch, err := archive.NewArchiver(pgPool, archiveCfg, sealer)
			if err != nil {
				logger.Error("telemetry archive", "err", err.Error())
				os.Exit(1)
			}
			logger.Info("telemetry archive on", "env", archiveCfg.Env, "endpoint", archiveCfg.Endpoint, "bucket", archiveCfg.Bucket)
			arch.Report = func(r archive.Report) {
				if r.Done {
					logger.Info("telemetry archive", "month", r.Month, "objects", r.Objects, "failed", r.Failed)
					return
				}
				if r.Err != nil {
					appMetrics.ArchiveObjectsTotal.WithLabelValues("failed").Inc()
					logger.Warn("telemetry archive: object failed", "month", r.Month, "vehicle_id", r.VehicleID.String(), "err", r.Err.Error())
					return
				}
				appMetrics.ArchiveObjectsTotal.WithLabelValues("verified").Inc()
				appMetrics.ArchiveBytesTotal.Add(float64(r.Bytes))
				logger.Info("telemetry archive: object verified", "month", r.Month, "vehicle_id", r.VehicleID.String(), "rows", r.Rows, "bytes", r.Bytes)
			}
			jobs = append(jobs, retention.Job{Name: "telemetry_archive", Run: arch.Run})
			// Queued deletions (account deletion, history reset) run hourly, so
			// objects are gone well within the 24-hour promise.
			del, err := archive.NewDeleter(pgPool, archiveCfg)
			if err != nil {
				logger.Error("telemetry archive deletions", "err", err.Error())
				os.Exit(1)
			}
			del.Report = func(r archive.DeleteReport) {
				appMetrics.ArchiveObjectsDeletedTotal.Add(float64(r.Objects))
				appMetrics.ArchiveDeletionsOverdue.Set(float64(r.Overdue))
				appMetrics.ArchiveDeletionsLastRun.SetToCurrentTime()
				if r.Requests > 0 || r.Failed > 0 || r.Overdue > 0 {
					logger.Info("telemetry archive deletions", "requests", r.Requests, "objects", r.Objects, "failed", r.Failed, "overdue", r.Overdue)
				}
			}
			appMetrics.ArchiveDeletionsLastRun.SetToCurrentTime()
			go archive.Every(ctx, time.Hour, "archive_deletions", logger, del.Run)
		}
		// vehicle_state past the retention period: whole monthly partitions are
		// dropped ("deleted" counts partitions). Drives, charges and their totals
		// are other tables and stay.
		if sweep := samples.NewRetention(pgPool, sampleRetentionMonths); sweep != nil {
			logger.Info("sample retention on", "months", sampleRetentionMonths)
			sweep.Dropped = func(name string, estRows int64) {
				logger.Info("sample retention: dropped partition", "partition", name, "rows_est", estRows)
				appMetrics.SamplePartitionsDropped.Inc()
			}
			jobs = append(jobs, retention.Job{Name: "vehicle_state_partitions", Run: sweep.Run})
		}
		// One old vehicle_state month a day thinned ("deleted" counts months);
		// its full copy is kept detached for 7 days for undo.
		if thinner := samples.NewThinner(pgPool, sampleThinAfterDays); thinner != nil {
			thinner.DryRun = os.Getenv("RIVOLT_SAMPLE_THIN_DRY_RUN") == "1"
			logger.Info("sample thinning on", "after_days", sampleThinAfterDays, "dry_run", thinner.DryRun)
			thinner.Report = func(r samples.ThinReport) {
				logger.Info("sample thinning", "partition", r.Partition, "dry_run", r.DryRun,
					"rows_before", r.RowsBefore, "rows_after", r.RowsAfter, "bytes_before", r.BytesBefore, "bytes_after", r.BytesAfter)
			}
			thinner.OnRethin = func(partition string, uid, vid uuid.UUID, deleted int64) {
				logger.Info("sample thinning: restored month thinned again", "partition", partition,
					"user_id", uid.String(), "vehicle_id", vid.String(), "rows_deleted", deleted)
			}
			thinner.OnRepair = func(partition string, flagged int64) {
				appMetrics.ArchiveRepairsTotal.Add(float64(flagged))
				logger.Warn("sample thinning: rows the archive lacks; flagged for re-archiving", "partition", partition, "objects", flagged)
			}
			jobs = append(jobs, retention.Job{Name: "vehicle_state_thinning", Run: thinner.Run})
		}
		go retention.Run(ctx, logger, jobs)
	}

	// The plug-in reminder (internal/plugreminder): an opt-in push, once a day,
	// when the truck is parked at the saved home place, unplugged and below the
	// user's level. Every replica sweeps; the engage claim makes it once. Never
	// in admin-only or demo-only mode, and never for the demo account.
	if pgPool != nil && pushSvc != nil && !adminOnlyMode && !demo.OnlyEnabled() {
		reminder := &plugreminder.Job{
			Source: plugreminder.NewSource(pgPool, func(ctx context.Context, uid uuid.UUID) (float64, float64, bool, error) {
				h, err := settings.GetHomeLocation(ctx, settingsFactory.For(uid))
				return h.Latitude, h.Longitude, h.Set, err
			}, demo.UserID()),
			Engage: &engage.Engage{Store: engage.NewStore(pgPool), Observe: func(kind, outcome string) {
				appMetrics.EngagementSendsTotal.WithLabelValues(kind, outcome).Inc()
			}},
			Logger: logger,
			On: func(ctx context.Context, uid uuid.UUID) (bool, error) {
				store := pushFactory.For(uid)
				if store == nil {
					return false, nil
				}
				subs, err := store.List(ctx)
				for _, sub := range subs {
					if sub.OnPlugInReminder {
						return true, err
					}
				}
				return false, err
			},
			Deliver: func(ctx context.Context, uid uuid.UUID, soc, threshold int) error {
				_, err := pushSvc.DeliverPlugInReminder(ctx, pushFactory.For(uid), soc, threshold)
				return err
			},
		}
		go reminder.Run(ctx)
	}

	// The left-open alert (internal/leftopen): an opt-in push when a door, the frunk, the liftgate, the tailgate or
	// the tonneau stays open after the truck is parked and left. A timer reads the monitor's latest state (a truck that
	// sleeps with the frunk open sends no frames); nothing here calls Rivian. A pod that does not run a vehicle's
	// monitor sees no state for it. Never in admin-only or demo-only mode, and never for the demo account.
	if pgPool != nil && pushSvc != nil && monitorRegistry != nil && !adminOnlyMode && !demo.OnlyEnabled() {
		leftOpen := &leftopen.Job{
			Source: leftopen.NewSource(pgPool, func(uid uuid.UUID, rivianID string) (*rivian.State, time.Time) {
				m := monitorRegistry.For(uid)
				if m == nil {
					return nil, time.Time{}
				}
				return m.Latest(rivianID)
			}, demo.UserID()),
			Deliver: func(ctx context.Context, uid uuid.UUID, model string, kinds []string, minutes int) error {
				_, err := pushSvc.DeliverLeftOpen(ctx, pushFactory.For(uid), model, kinds, minutes)
				return err
			},
			Logger: logger,
		}
		go leftOpen.Run(ctx)
	}

	// Signs the monthly statement email's one-click unsubscribe links.
	var statementSigner, weeklySigner *statement.Signer
	if len(cookieSecret) > 0 {
		weeklySigner = statement.NewScopedSigner(cookieSecret, "weekly")
		statementSigner = statement.NewSigner(cookieSecret)
		onboardSigner.Store(statementSigner)
	}
	// The backup download reads thinned months back from the telemetry
	// archive. A typed nil would make a non-nil interface, so set it only
	// when there is a reader.
	var archiveRows samples.ArchiveRows
	var archiveRestorer api.ArchiveRestorer
	if archiveOn && pgPool != nil {
		// Request pool: the backup reads the archive as the signed-in user, so row-level security backstops a query that drops its
		// user filter. A request that lost its user fails the backup (samples.ErrBackupUnavailable) rather than returning no archive.
		rd, err := archive.NewReader(requestDB, archiveCfg, sealer)
		if err != nil {
			logger.Error("telemetry archive reader", "err", err.Error())
			os.Exit(1)
		}
		rd.OnCorrupt = func(uid, vid uuid.UUID, month time.Time, err error) {
			appMetrics.ArchiveReadCorruptTotal.Inc()
			logger.Error("telemetry archive: object failed to read back", "user_id", uid.String(), "vehicle_id", vid.String(),
				"month", archive.MonthString(month), "err", err.Error())
		}
		archiveRows = rd
		rs, err := archive.NewRestorer(pgPool, archiveCfg, sealer)
		if err != nil {
			logger.Error("telemetry archive restorer", "err", err.Error())
			os.Exit(1)
		}
		archiveRestorer = rs
	}
	// What the identity provider remembers about a user (Hydra's remembered logins, Kratos' sessions): revoking sessions must forget it
	// too, or the browser signs straight back in. Shared by sign-out-others, sign-out, the password routes and support's secure-account.
	rememberedRevoker := &api.IdentityRevoker{
		Hydra: hydraClient, Kratos: kratosClient,
		Username: func(ctx context.Context, uid uuid.UUID) (string, error) { return db.RawUsernameByID(ctx, pgPool, uid) }, // system pool
	}
	var sessionRevoker interface {
		RevokeAll(ctx context.Context, userID uuid.UUID) (int64, error)
	}
	if sessionStore != nil {
		sessionRevoker = sessionStore // a nil *Store in the interface would not be nil
	}
	var sessionsAPI *api.SessionsAPI
	if sessionStore != nil {
		sessionsAPI = &api.SessionsAPI{
			Store: sessionStore,
			Current: func(r *http.Request) (uuid.UUID, bool) {
				ref, ok := auth.SessionFromContext(r.Context())
				return ref.ID, ok
			},
			Remembered: rememberedRevoker,
			Observe:    api.NewForgetObserver(appMetrics),
		}
	}
	handler := api.New(api.Deps{
		AuditKey:              deriveKey(cookieSecret, "rivolt/audit/v1"),
		Sessions:              sessionsAPI,
		SampleRetentionMonths: sampleRetentionMonths,
		ArchiveRows:           archiveRows,
		ArchiveRestorer:       archiveRestorer,
		StatementSigner:       statementSigner,
		WeeklySigner:          weeklySigner,
		Rivian:                rivianClient,
		Accounts:              accountRegistry,
		PushService:           pushSvc,
		Drives:                reqStores.Drives,
		Charges:               reqStores.Charges,
		Samples:               reqStores.Samples,
		Settings:              reqStores.Settings,
		SystemSettings:        settingsFactory, // the sweeps started in api.New (statement and weekly emails) run with no user in context
		Push:                  reqStores.Push,
		Trips:                 reqStores.Trips,
		PackHealth:            reqStores.PackHealth, // request pool: per-vehicle samples reached through the vehicle's owner (the monitor's own store stays on the system pool)
		Monitors:              monitorRegistry,
		SettingsMgr:           settingsMgr,
		Auth:                  authSvc,
		AuthEnforced:          authEnforced,
		ImpersonationDisabled: impersonationDisabled,
		AdminDisabled:         adminDisabled,
		AdminOnly:             adminOnlyMode,
		PlanLegs:              newPlanLegs(sharedRedis, redisKeyPrefix, logger, appMetrics, replicaDivisor),
		Redis:                 sharedRedis,
		RedisPrefix:           redisKeyPrefix,
		RivianExpiryEmails:    rivianExpiryEmails,
		FailLimiterKey:        api.FailLimiterKey(cookieSecret),
		PurgeUserCaches:       func(_ context.Context, uid uuid.UUID) { purgeUserCaches(uid) },
		AdminHealth: adminhealth.New(adminhealth.Config{
			DB: pgPool, Flags: flagsStore, Version: version,
			AlertmanagerURL: os.Getenv("RIVOLT_ALERTMANAGER_URL"),
			PrometheusURL:   os.Getenv("RIVOLT_PROMETHEUS_URL"),
			Namespace:       os.Getenv("RIVOLT_HEALTH_NAMESPACE"),
		}),
		Access:             accessVerifier,
		AdminDebugProbes:   adminDebugProbes,
		OIDC:               oidcSvc,
		AppleNotifications: appleNotifier,
		AppAuth:            appAuth,
		IOSAppIDs:          iosAppIDs,
		WebFS:              webFS,
		Version:            version,
		DB:                 requestDB,         // request handling (the request pool when RIVOLT_REQUEST_DATABASE_URL is set)
		SystemDB:           pgPool,            // login, admin and the other system paths (S2-3 moves them here one by one)
		LoginForgetter:     rememberedRevoker, // plain sign-out also forgets Hydra's remembered login (shared computers)
		SessionRevoker:     sessionRevoker,    // support's "secure this account" revokes every session and app token
		RememberedRevoker:  rememberedRevoker,
		RequestPoolOn:      requestDB != pgPool,
		Logger:             logger,
		Flags:              flagsStore,
		AIBudget:           reqStores.AIBudget, // request pool: one (user_id, day) row per caller
		Secrets:            reqStores.Secrets,  // request pool: the Rivian sign-in surface reads and writes the caller's own session
		SystemSecrets:      secretsStore,       // admin refresh of another user's Rivian session
		Metrics:            appMetrics,
		Users:              userProvider,
		AppleRevocation:    appleRevocation,
		SignupRequests:     signupRequestStore,
		Email:              mailer,
		BaseURL:            publicBaseURL,
		// Release notes for /changelog and /changelog.atom, from the public
		// GitHub Releases (fixed URL, cached). RIVOLT_CHANGELOG=false turns
		// them off; the admin deployment never serves them.
		Changelog:        changelogSource(adminOnlyMode),
		ValhallaProxy:    valhallaProxy,
		Valhalla:         maps.NewValhalla(os.Getenv("RIVOLT_VALHALLA_BASE_URL")), // the server calls it itself (overnight search); nil without the URL
		TilesProxy:       tilesProxy,
		ChargersArchive:  chargersArchive,
		Photon:           photonClient,
		WeatherClient:    weather.NewClient(),
		WeatherCache:     weather.NewMemCache(15*time.Minute, 1024),
		Hydra:            hydraClient,
		Kratos:           kratosClient,
		Passkeys:         passkeySvc,
		PasskeyStore:     passkeyStore,
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

	if metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", appMetrics.Handler())
		msrv := &http.Server{Addr: metricsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			logger.Info("metrics listening", "addr", metricsAddr)
			if err := msrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics server error", "err", err.Error())
			}
		}()
		defer func() { _ = msrv.Close() }()
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

	// Hand the signals back, then treat a second one as "now": the first started a
	// graceful stop, and the NotifyContext above would otherwise swallow every
	// later SIGTERM until we return.
	stop()
	second := make(chan os.Signal, 1)
	signal.Notify(second, syscall.SIGINT, syscall.SIGTERM)
	go exitOnSecondSignal(second, os.Exit, logger)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpDrainTimeout)
	defer cancel()
	// Release leases BEFORE the HTTP server shuts down so peers
	// can pick the vehicles up while we're still draining
	// in-flight requests. ReleaseAll uses a short bounded context;
	// a Postgres blip just delays acquisition until the TTL.
	// Write the drives and charges the 10 s throttle left behind first, so the
	// peer that takes the vehicles over finds current rows.
	if monitorRegistry != nil {
		// One deadline for the whole drain flush: the per-vehicle hand-off flushes
		// that follow (Unsubscribe) share it instead of taking their own.
		deadline := time.Now().Add(drainFlushTimeout)
		rivian.BeginDrain(deadline)
		fctx, fcancel := context.WithDeadline(context.Background(), deadline)
		if n := monitorRegistry.FlushPending(fctx); n > 0 {
			logger.Info("flushed in-progress sessions at shutdown", "sessions", n)
		}
		fcancel()
	}
	if leaseCoordinator != nil {
		lctx, lcancel := context.WithTimeout(context.Background(), leaseReleaseTimeout)
		leaseCoordinator.Shutdown(lctx)
		lcancel()
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown error", "err", err.Error())
	}
	// Let the detached work (re-auth email, close hooks, vehicle-info writes) finish
	// before the database goes away. Bounded: 3 s leases + 10 s HTTP + 10 s here fits
	// the 30 s grace period.
	if !bg.Default.Wait(bgShutdownWait) {
		logger.Warn("background work still running at shutdown; closing anyway", "waited", bgShutdownWait.String())
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
	// RIVOLT_KEK_ROTATION holds the retired KEKs' VALUES ("<id>:<base64>",
	// comma-separated), not names of other variables; a malformed entry refuses
	// to boot rather than leave rows sealed under that key unreadable.
	var retired []string
	for _, v := range strings.Split(os.Getenv("RIVOLT_KEK_ROTATION"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			retired = append(retired, v)
		}
	}
	sealer, err := rivoltcrypto.NewEnvSealerWithRotation(os.Getenv("RIVOLT_KEK"), retired)
	if err != nil {
		return nil, err
	}
	primary, kept := sealer.KEKIDs()
	logger.Info("KEKs loaded", "primary", primary, "retired", kept)
	return sealer, nil
}

// sendReauthEmail notifies a user that their stored Rivian session was
// rejected and recording has stopped. Fired exactly once per episode
// from the reauth sink's rising edge (the DB transition gate dedupes
// across replicas). Best-effort: a missing email/mailer or a Resend
// blip is logged, never retried — the in-app banner and admin badge
// remain the durable signals.
func sendReauthEmail(ctx context.Context, mailer *email.Client, pool *sql.DB, logger *slog.Logger, uid uuid.UUID, reason, baseURL string) {
	if mailer == nil || pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	to, err := db.LookupUserEmail(ctx, pool, uid)
	if err != nil {
		logger.Warn("reauth email: lookup email", "user_id", uid.String(), "err", err.Error())
		return
	}
	if to == "" {
		return
	}
	link := strings.TrimRight(baseURL, "/") + "/settings#rivian"
	if baseURL == "" {
		link = "your Rivolt settings"
	}
	body := "Rivolt lost access to your Rivian account, so your drives and\n" +
		"charging sessions have stopped recording.\n\n" +
		"This usually means Rivian expired the stored session. To fix it,\n" +
		"sign in to Rivian again here:\n\n" +
		"  " + link + "\n\n" +
		"Your historical data is safe — reconnecting resumes recording from\n" +
		"where it left off.\n"
	if err := mailer.Send(ctx, email.Message{
		To:      to,
		Subject: "Action needed: reconnect your Rivian account to Rivolt",
		Text:    body,
	}); err != nil {
		logger.Warn("reauth email: send", "user_id", uid.String(), "err", err.Error())
		return
	}
	logger.Info("reauth email sent", "user_id", uid.String(), "reason", reason)
}

// breakerMetrics adapts rivian.BreakerObserver onto the Prometheus
// gauge + counter on appMetrics. Kept in main.go (not the metrics
// package) so the metrics package stays import-free of rivian; the
// dependency direction is rivian → main → metrics, never the other
// way.
// sleepMetrics adapts rivian.SleepObserver onto appMetrics, for the
// same dependency-direction reason as breakerMetrics below.
type sleepMetrics struct{ m *metrics.Metrics }

func (s *sleepMetrics) OnSleepEnded(_ string, _ time.Duration, failed bool) {
	if s == nil || s.m == nil {
		return
	}
	outcome := "lasted"
	if failed {
		outcome = "failed"
	}
	s.m.VehicleSleepsTotal.WithLabelValues(outcome).Inc()
}

// OnChargeEnergyFloor implements rivian.ChargeEnergyObserver.
func (s *sleepMetrics) OnChargeEnergyFloor() {
	if s == nil || s.m == nil {
		return
	}
	s.m.ChargeEnergyFloorTotal.Inc()
}

// OnOTAStatus implements rivian.OTAObserver. class is from a fixed list or "other" (bounded cardinality).
func (s *sleepMetrics) OnOTAStatus(class string) {
	if s == nil || s.m == nil {
		return
	}
	s.m.OTAStatusFramesTotal.WithLabelValues(class).Inc()
}

// OnTireStatus implements rivian.TireObserver. class is one of low, not_low, other, missing (bounded cardinality).
func (s *sleepMetrics) OnTireStatus(class string) {
	if s == nil || s.m == nil {
		return
	}
	s.m.TireStatusTotal.WithLabelValues(class).Inc()
}

// OnParallaxReplayIgnored implements rivian.ParallaxReplayObserver.
// topic is one of a few fixed Parallax RVM names (bounded cardinality).
func (s *sleepMetrics) OnParallaxReplayIgnored(topic string) {
	if s == nil || s.m == nil {
		return
	}
	s.m.ParallaxReplaysIgnoredTotal.WithLabelValues(topic).Inc()
}

// rivianResultMetrics counts each Rivian call's outcome by operation and
// class (rivolt_rivian_results_total); same dependency direction as
// breakerMetrics.
type rivianResultMetrics struct{ m *metrics.Metrics }

func (r *rivianResultMetrics) OnResult(op, class string) {
	if r == nil || r.m == nil {
		return
	}
	r.m.RivianResultsTotal.WithLabelValues(op, class).Inc()
}

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
	l interface {
		Allow(ctx context.Context, class ratelimit.Class) (bool, time.Duration)
	}
	m *metrics.Metrics
}

func (r *rateLimitMetrics) Allow(ctx context.Context, class string) (bool, time.Duration) {
	ok, retry := r.l.Allow(ctx, ratelimit.Class(class))
	if !ok && r.m != nil {
		r.m.RivianRateLimitBlocked.WithLabelValues(class).Inc()
	}
	return ok, retry
}

// emailMetricsAdapter forwards email.Client send outcomes into the
// appMetrics counter. Same dependency-inversion shape as the other
// adapters above: internal/email defines the Counter interface so it
// stays import-free; main owns the actual metric.
type emailMetricsAdapter struct{ m *metrics.Metrics }

func (a emailMetricsAdapter) IncEmailSend(provider, status string) {
	if a.m == nil {
		return
	}
	a.m.EmailSendTotal.WithLabelValues(provider, status).Inc()
}

// parseReplayBound reads an optional RIVOLT_REPLAY_FROM/_TO value:
// RFC 3339 or a bare YYYY-MM-DD (UTC midnight). Empty means unbounded.
func parseReplayBound(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", v)
}

// changelogSource is the release-notes source, or nil when they are off.
func changelogSource(adminOnly bool) *changelog.Source {
	if adminOnly || strings.EqualFold(strings.TrimSpace(os.Getenv("RIVOLT_CHANGELOG")), "false") {
		return nil
	}
	return changelog.New("")
}

// hostOnly strips the port from an "ip:port" address (the sessions store
// records the bare IP).
func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// deriveKey is a key for one purpose, derived from the server's secret (HMAC-SHA256 of the purpose label); nil when the
// secret is empty.
func deriveKey(secret []byte, purpose string) []byte {
	if len(secret) == 0 {
		return nil
	}
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write([]byte(purpose))
	return m.Sum(nil)
}

// appleLinks adapts appleid.Store to oidc.AppleLinks (the oidc package doesn't import the store).
type appleLinks struct{ *appleid.Store }

func (a appleLinks) Link(ctx context.Context, uid uuid.UUID, sub, refreshToken string) error {
	err := a.Store.Link(ctx, uid, sub, refreshToken)
	if errors.Is(err, appleid.ErrSubInUse) {
		return oidc.ErrAppleLinkConflict
	}
	return err
}

// newPlanLegs builds the planner cache and quota. Without Redis (or while it is down) the
// quota is counted in the process, and each such check is counted.
func newPlanLegs(rdb *redis.Client, prefix string, logger *slog.Logger, m *metrics.Metrics, divisor float64) *planleg.Cache {
	c := planleg.New(rdb, prefix, logger)
	c.FallbackDivisor = divisor
	if m != nil {
		c.OnFallback = m.PlanQuotaFallbackTotal.Inc
	}
	return c
}

// replicaDivisorFromEnv reads RIVOLT_REPLICA_DIVISOR (1 to 100); anything else is 3, the number
// of pods we run with a surge pod.
func replicaDivisorFromEnv(getenv func(string) string) float64 {
	if n, err := strconv.Atoi(strings.TrimSpace(getenv("RIVOLT_REPLICA_DIVISOR"))); err == nil && n >= 1 && n <= 100 {
		return float64(n)
	}
	return 3
}
