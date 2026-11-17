package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/apohor/rivolt/internal/adminaudit"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/flags"
)

// opsActor is the actor the flag row and the audit row carry for a change made here, not from the admin site.
const opsActor = "ops-cli"

// rivolt live-destination-users: the operator's way to name who the live_destination flag is on for, for when the admin site (behind
// Cloudflare Access) is out of reach. It goes through the same store function as PUT /api/admin/flags/live-destination, so the same
// rule holds: user UUIDs only, never "all". It prints counts, never ids. A dry run unless --apply, and --apply writes an
// admin_audit row (action flag.live_destination, actor ops-cli) first and refuses to change anything when that row cannot be written.
func runLiveDestinationUsers(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("live-destination-users", flag.ContinueOnError)
	fs.SetOutput(stderr)
	show := fs.Bool("show", false, "print how many users the flag is on for")
	add := fs.String("add", "", "add this user UUID")
	remove := fs.String("remove", "", "remove this user UUID")
	apply := fs.Bool("apply", false, "write the change (default: dry run)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "live-destination-users: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	n := 0
	for _, set := range []bool{*show, *add != "", *remove != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		fmt.Fprintln(stderr, "usage: rivolt live-destination-users --show | --add UUID [--apply] | --remove UUID [--apply]")
		return 2
	}
	var op, id string
	switch {
	case *add != "":
		op, id = "add", *add
	case *remove != "":
		op, id = "remove", *remove
	}
	if op != "" {
		ids, err := flags.ParseUserIDs([]string{id})
		if err != nil {
			fmt.Fprintln(stderr, "live-destination-users:", err)
			return 2
		}
		id = ids[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(stderr, "live-destination-users: postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := changeLiveDestinationUsers(ctx, pool, op, id, *apply, stdout); err != nil {
		fmt.Fprintln(stderr, "live-destination-users:", err)
		return 1
	}
	return 0
}

// changeLiveDestinationUsers is the work of the command: op is "" (show), "add" or "remove", id a normalised UUID. Output is counts only.
func changeLiveDestinationUsers(ctx context.Context, pool *sql.DB, op, id string, apply bool, out io.Writer) error {
	st, err := flags.OpenStore(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	current := slices.Clone(st.LiveDestination().Users)
	slices.Sort(current)
	if op == "" {
		fmt.Fprintf(out, "live_destination: on for %d users\n", len(current))
		return nil
	}
	next := slices.DeleteFunc(slices.Clone(current), func(u string) bool { return u == id })
	if op == "add" {
		next = append(next, id)
	}
	slices.Sort(next)
	if slices.Equal(next, current) {
		fmt.Fprintf(out, "live_destination: no change, on for %d users\n", len(current))
		return nil
	}
	if !apply {
		fmt.Fprintf(out, "dry run: live_destination would go from %d to %d users (%s); pass --apply to write\n", len(current), len(next), op)
		return nil
	}
	audit := adminaudit.New(pool)
	row, err := audit.Begin(ctx, adminaudit.Entry{
		Actor: db.UserIDFor(opsActor), Action: "flag.live_destination", TargetType: "flag", TargetID: "live_destination",
		Details: map[string]any{"actor": opsActor, "op": op, "users_count": len(next), "before_users_count": len(current)},
	})
	if err != nil {
		return fmt.Errorf("the audit log is unavailable, so the change was not made: %w", err)
	}
	status := 200
	err = st.SetLiveDestination(ctx, next, opsActor)
	if err != nil {
		status = 500
	}
	if ferr := audit.Finish(context.WithoutCancel(ctx), row, status); ferr != nil {
		fmt.Fprintln(out, "warning: could not record the outcome in the audit log")
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "live_destination: %s done, on for %d users (was %d)\n", op, len(next), len(current))
	return nil
}
