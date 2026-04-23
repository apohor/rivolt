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

	var rivianClient rivian.Client = rivian.NewStub()

	webFS := web.Assets()
	if webFS == nil {
		logger.Warn("embedded web bundle missing; SPA routes will 404 until `make web` is run")
	}

	handler := api.New(api.Deps{
		Rivian:      rivianClient,
		PushService: pushSvc,
		PushStore:   pushStore,
		SettingsMgr: settingsMgr,
		WebFS:       webFS,
		Version:     version,
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
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
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

	imp := &electrafi.Importer{Drives: ds, Charges: cs, VehicleID: *vehicleID}
	ctx := context.Background()
	var totalRows, totalDrives, totalCharges, totalSkipped int
	for _, f := range files {
		res, err := imp.Import(ctx, f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "import %s: %v\n", f, err)
			os.Exit(1)
		}
		fmt.Printf("%s: rows=%d drives=%d charges=%d skipped=%d\n",
			f, res.Rows, res.Drives, res.Charges, res.SkippedRows)
		totalRows += res.Rows
		totalDrives += res.Drives
		totalCharges += res.Charges
		totalSkipped += res.SkippedRows
	}
	fmt.Printf("total: rows=%d drives=%d charges=%d skipped=%d\n",
		totalRows, totalDrives, totalCharges, totalSkipped)
}
