package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/adminaudit"
	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/db"
)

// runArchive is `rivolt archive restore --user U --vehicle V --month 2026-05
// [--apply]`: put one archived vehicle-month back in full detail. A dry run
// unless --apply; it waits for the maintenance lock. Audited like the admin
// site's restore (actor: none, ip: cli).
func runArchive(args []string) int {
	if len(args) == 0 || args[0] != "restore" {
		fmt.Fprintln(os.Stderr, "usage: rivolt archive restore --user ID --vehicle ID --month YYYY-MM [--apply]")
		return 2
	}
	fs := flag.NewFlagSet("archive restore", flag.ContinueOnError)
	user := fs.String("user", "", "user id")
	vehicle := fs.String("vehicle", "", "vehicle id (vehicles.id)")
	monthStr := fs.String("month", "", "month, YYYY-MM")
	apply := fs.Bool("apply", false, "write (default: dry run)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	uid, err1 := uuid.Parse(*user)
	vid, err2 := uuid.Parse(*vehicle)
	month, err3 := time.Parse("2006-01", *monthStr)
	if err1 != nil || err2 != nil || err3 != nil {
		fmt.Fprintln(os.Stderr, "archive restore: --user and --vehicle must be ids, --month YYYY-MM")
		return 2
	}
	cfg, on, err := archive.ConfigFromEnv(os.Getenv)
	if err != nil || !on {
		fmt.Fprintln(os.Stderr, "archive restore: the telemetry archive isn't configured (RIVOLT_ARCHIVE_*):", err)
		return 1
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sealer, err := buildSealer(logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archive restore: sealer:", err)
		return 1
	}
	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "archive restore: postgres:", err)
		return 1
	}
	defer pool.Close()
	rs, err := archive.NewRestorer(pool, cfg, sealer)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archive restore:", err)
		return 1
	}
	audit := adminaudit.New(pool)
	id, err := audit.Begin(ctx, adminaudit.Entry{
		Action: "archive.restore", TargetType: "archive_month", TargetID: uid.String(), TargetUser: uid, IP: "cli",
		Details: map[string]any{"vehicle_id": vid.String(), "month": archive.MonthString(month), "dry_run": !*apply},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "archive restore: audit:", err)
		return 1
	}
	res, err := rs.Restore(ctx, uid, vid, month, !*apply, true)
	status := 200
	if err != nil {
		status = 500
	}
	_ = audit.Finish(ctx, id, status)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archive restore:", err)
		return 1
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	return 0
}
