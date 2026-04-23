// Command rivolt is the single-binary server for the Rivolt Rivian companion.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	// Embed the IANA time zone database so TZ=America/New_York etc. work
	// even on distroless images that don't ship /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/charges"
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
	dbPath := filepath.Join(*dataDir, "rivolt.db")

	settingsStore, err := settings.OpenStore(dbPath)
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

	pushStore, err := push.OpenStore(dbPath)
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

	drivesStore, err := drives.OpenStore(dbPath)
	if err != nil {
		logger.Warn("drives store unavailable", "err", err.Error())
	}
	chargesStore, err := charges.OpenStore(dbPath)
	if err != nil {
		logger.Warn("charges store unavailable", "err", err.Error())
	}
	samplesStore, err := samples.OpenStore(dbPath)
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
		// fallback uses DefaultPackKWh (141.5) and the UI has no
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
	dataDir := fs.String("data-dir", envOr("DATA_DIR", "./data"), "directory holding rivolt.db")
	vehicleID := fs.String("vehicle-id", envOr("RIVOLT_VEHICLE_ID", ""), "vehicle_id to attribute sessions to (default: derived from filename)")
	packKWh := fs.Float64("pack-kwh", envFloat("RIVOLT_PACK_KWH", electrafi.DefaultPackKWh), "usable pack capacity in kWh; used to estimate energy when ElectraFi omits charger_power")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	files := fs.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rivolt import electrafi <file.csv> [<file.csv>...]")
		os.Exit(2)
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "data dir: %v\n", err)
		os.Exit(1)
	}
	dbPath := filepath.Join(*dataDir, "rivolt.db")

	ds, err := drives.OpenStore(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open drives: %v\n", err)
		os.Exit(1)
	}
	defer ds.Close()
	cs, err := charges.OpenStore(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open charges: %v\n", err)
		os.Exit(1)
	}
	defer cs.Close()
	ss, err := samples.OpenStore(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open samples: %v\n", err)
		os.Exit(1)
	}
	defer ss.Close()

	imp := &electrafi.Importer{Drives: ds, Charges: cs, Samples: ss, VehicleID: *vehicleID, PackKWh: *packKWh}
	ctx := context.Background()
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
