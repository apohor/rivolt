package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/launchnotice"
	"github.com/apohor/rivolt/internal/logging"
)

// staleRetry is how long after its row was created an unsent email may still be retried: a retry states the dates the first attempt
// stated, so it must not run long after them.
const staleRetry = 7 * 24 * time.Hour

// noticeStore is the slice of launchnotice.Store the command uses, so tests run on a fake.
type noticeStore interface {
	Ensure(ctx context.Context, uid uuid.UUID, kind string, p launchnotice.Params) (bool, error)
	ClaimEmail(ctx context.Context, uid uuid.UUID, kind string) (launchnotice.Params, bool, error)
	MarkSent(ctx context.Context, uid uuid.UUID, kind string) error
	ReleaseClaim(ctx context.Context, uid uuid.UUID, kind string) error
}

// runLaunchNotice is the operator command that gives every account the Terms section 11 notice that Rivolt has a free tier and Pro:
// an in-app notice for every account that can sign in, and a service email to every verified address, whatever the account's engagement
// settings (it is a legal notice, not a message the engagement budget governs).
//
// Dry run by default: it prints counts only, never an id, prefix or address. --apply creates the notices and sends the emails, one
// message each, at --rate per second. It is idempotent per account: the in-app row is created once, and the email is claimed, sent and
// marked, so a re-run only retries failures. --test-to USER_ID sends the email to that one account and writes nothing.
//
// The dates come from --launch-date alone: the founding year ends a year after it, and the new Terms take effect 30 days after it. The
// notice must be sent on or before the launch date, so the stated Terms date is never fewer than 30 days after the email. A row keeps
// the dates it was created with; a retry states those, and is refused once the row is more than a week old.
func runLaunchNotice(args []string) int {
	fs := flag.NewFlagSet("launch-notice", flag.ContinueOnError)
	launchDate := fs.String("launch-date", "", "the launch date, YYYY-MM-DD (required); send on or before it")
	apply := fs.Bool("apply", false, "create the notices and send the emails (default: dry run, counts only)")
	testTo := fs.String("test-to", "", "send the email only to this user id and write nothing")
	rate := fs.Float64("rate", 2, "emails per second")
	var exclude repeatedFlag
	fs.Var(&exclude, "exclude-user", "user id or username to leave out (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	launch, err := time.Parse("2006-01-02", strings.TrimSpace(*launchDate))
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch-notice: --launch-date YYYY-MM-DD is required")
		return 2
	}
	if *rate <= 0 || *rate > 10 {
		fmt.Fprintln(os.Stderr, "launch-notice: --rate must be in (0, 10]")
		return 2
	}
	only := uuid.Nil
	if *testTo != "" {
		if only, err = uuid.Parse(strings.TrimSpace(*testTo)); err != nil {
			fmt.Fprintln(os.Stderr, "launch-notice: --test-to must be a user id (uuid)")
			return 2
		}
	}
	skipIDs := map[uuid.UUID]bool{demo.UserID(): true}
	if bu := strings.TrimSpace(os.Getenv("RIVOLT_AUTH_BYPASS_USER")); bu != "" {
		skipIDs[db.UserIDFor(bu)] = true
	}
	excluded := map[uuid.UUID]bool{}
	for _, e := range exclude {
		e = strings.TrimSpace(e)
		if id, err := uuid.Parse(e); err == nil {
			excluded[id] = true
		} else if e != "" {
			excluded[db.UserIDFor(e)] = true
		}
	}

	ctx := context.Background()
	pool, err := db.OpenNoMigrate(ctx, postgresDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch-notice: postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := db.RequireTables(ctx, pool, "users", "account_notices"); err != nil {
		fmt.Fprintln(os.Stderr, "launch-notice:", err)
		return 1
	}
	var mailer announceMailer
	c := email.New(email.Config{APIKey: os.Getenv("RIVOLT_RESEND_API_KEY"), From: os.Getenv("RIVOLT_EMAIL_FROM")})
	if *apply || only != uuid.Nil {
		if c == nil {
			fmt.Fprintln(os.Stderr, "launch-notice: mailer not configured (RIVOLT_RESEND_API_KEY / RIVOLT_EMAIL_FROM)")
			return 1
		}
		mailer = c
	}
	st := launchnotice.Store{DB: pool}
	rows, err := listAnnounceUsers(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch-notice: users:", err)
		return 1
	}
	states, err := st.States(ctx, launchnotice.Kind)
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch-notice: notices:", err)
		return 1
	}
	return launchNoticeRun(ctx, launchNoticeDeps{
		Rows: rows, States: states, Store: st, Mailer: mailer, Out: os.Stdout,
		Logger: slog.New(slog.NewJSONHandler(os.Stderr, nil)), Sleep: time.Sleep, Now: time.Now,
		SkipIDs: skipIDs, Excluded: excluded, Only: only, Apply: *apply, Launch: launch,
		Interval: time.Duration(float64(time.Second) / *rate),
	})
}

type launchNoticeDeps struct {
	Rows     []announceUserRow
	States   map[uuid.UUID]launchnoticeState
	Store    noticeStore
	Mailer   announceMailer
	Out      io.Writer
	Logger   *slog.Logger
	Sleep    func(time.Duration)
	Now      func() time.Time
	SkipIDs  map[uuid.UUID]bool
	Excluded map[uuid.UUID]bool
	Only     uuid.UUID
	Apply    bool
	Launch   time.Time
	Interval time.Duration
}

type launchnoticeState = launchnotice.State

// noticeCounts is everything the command reports: counts, never identities.
type noticeCounts struct {
	Considered                                            int
	Disabled, BypassDemo, Excluded                        int
	InAppToCreate, InAppAlready                           int
	EmailToSend, EmailAlready, NoVerifiedAddress, BadAddr int
	DatesMismatch, StaleRetry                             int
	// outcomes of --apply
	InAppCreated, EmailSent, EmailFailed, EmailSkipped int
}

func launchNoticeRun(ctx context.Context, d launchNoticeDeps) int {
	p := launchnotice.NewParams(d.Launch)
	if err := p.Check(); err != nil {
		fmt.Fprintln(d.Out, "launch-notice:", err)
		return 2
	}
	now := d.Now()
	today := now.UTC().Truncate(24 * time.Hour)
	if d.Apply && today.After(d.Launch.UTC()) {
		fmt.Fprintf(d.Out, "launch-notice: refusing --apply: today (%s UTC) is after the launch date %s. The new Terms take effect %s; sent today that would be fewer than 30 days' notice. Move the launch date or stop.\n",
			today.Format("2006-01-02"), p.Launch, p.TermsEffective)
		return 1
	}

	// Classify.
	var c noticeCounts
	var targets []noticeTarget
	for _, u := range d.Rows {
		if d.Only != uuid.Nil && u.ID != d.Only {
			continue
		}
		c.Considered++
		switch {
		case d.SkipIDs[u.ID]:
			c.BypassDemo++
			continue
		case d.Excluded[u.ID]:
			c.Excluded++
			continue
		case u.Disabled:
			c.Disabled++
			continue
		}
		st := d.States[u.ID]
		em := u.Verified && deliverable(u.Email)
		switch {
		case !u.Verified:
			c.NoVerifiedAddress++
		case !deliverable(u.Email):
			c.BadAddr++
		}
		if st.Exists {
			c.InAppAlready++
			if st.Params != p {
				c.DatesMismatch++
			}
		} else {
			c.InAppToCreate++
		}
		if em {
			if st.EmailSent {
				c.EmailAlready++
			} else {
				c.EmailToSend++
				if st.Exists && now.Sub(st.CreatedAt) > staleRetry {
					c.StaleRetry++
				}
			}
		}
		targets = append(targets, noticeTarget{u, st, em})
	}
	if d.Only != uuid.Nil {
		return launchNoticeTest(ctx, d, p, targets)
	}

	mode := "dry run"
	if d.Apply {
		mode = "applying"
	}
	fmt.Fprintf(d.Out, "launch-notice (%s)\n", mode)
	fmt.Fprintf(d.Out, "dates stated: launch %s, free Pro year ends %s, new Terms take effect %s (30 days after the launch date; send on or before %s)\n",
		p.Launch, p.FoundingEnd, p.TermsEffective, p.Launch)
	fmt.Fprintf(d.Out, "accounts considered %d; left out: disabled %d, demo/bypass %d, excluded %d\n", c.Considered, c.Disabled, c.BypassDemo, c.Excluded)
	fmt.Fprintf(d.Out, "in-app notice: to create %d, already there %d\n", c.InAppToCreate, c.InAppAlready)
	fmt.Fprintf(d.Out, "email: to send %d, already sent %d; in-app only: unverified address %d, undeliverable address %d\n",
		c.EmailToSend, c.EmailAlready, c.NoVerifiedAddress, c.BadAddr)
	if c.DatesMismatch > 0 || c.StaleRetry > 0 {
		fmt.Fprintf(d.Out, "REFUSED: %d existing notice(s) state different dates than --launch-date gives; %d unsent email(s) are on rows more than %d days old. A retry must state the dates the first attempt did, and soon after it.\n",
			c.DatesMismatch, c.StaleRetry, int(staleRetry.Hours()/24))
		return 1
	}
	if !d.Apply {
		fmt.Fprintln(d.Out, "dry run; pass --apply to create the notices and send the emails (or --test-to USER_ID for one test email)")
		return 0
	}

	sentOne := false
	for _, t := range targets {
		created, err := d.Store.Ensure(ctx, t.u.ID, launchnotice.Kind, p)
		if err != nil {
			d.Logger.Warn("launch-notice: notice not written", "user_id", t.u.ID.String(), "err", logging.ScrubErr(err))
			c.EmailFailed++
			continue
		}
		if created {
			c.InAppCreated++
		}
		if !t.emailable || t.st.EmailSent {
			continue
		}
		sp, claimed, err := d.Store.ClaimEmail(ctx, t.u.ID, launchnotice.Kind)
		if err != nil {
			d.Logger.Warn("launch-notice: claim failed", "user_id", t.u.ID.String(), "err", logging.ScrubErr(err))
			c.EmailFailed++
			continue
		}
		if !claimed {
			c.EmailSkipped++
			continue
		}
		if sentOne && d.Sleep != nil {
			d.Sleep(d.Interval)
		}
		sentOne = true
		body := launchnotice.RenderEmail(sp)
		if tok := launchnotice.Unfilled(body); tok != "" {
			_ = d.Store.ReleaseClaim(ctx, t.u.ID, launchnotice.Kind)
			fmt.Fprintf(d.Out, "launch-notice: the rendered email still has %q; stopping\n", tok)
			return 1
		}
		err = d.Mailer.Send(ctx, email.Message{To: strings.TrimSpace(t.u.Email), Subject: launchnotice.Subject, Text: body,
			IdempotencyKey: "launch-notice-" + t.u.ID.String()})
		if err != nil {
			_ = d.Store.ReleaseClaim(ctx, t.u.ID, launchnotice.Kind)
			c.EmailFailed++
			d.Logger.Warn("launch-notice: send failed", "user_id", t.u.ID.String(), "err", logging.ScrubErr(err))
			continue
		}
		if err := d.Store.MarkSent(ctx, t.u.ID, launchnotice.Kind); err != nil {
			// Sent but not recorded: stop, so a re-run (which Resend's idempotency key covers for 24 hours) is the operator's decision.
			d.Logger.Error("launch-notice: sent but not recorded; stopping", "user_id", t.u.ID.String(), "err", logging.ScrubErr(err))
			c.EmailFailed++
			fmt.Fprintf(d.Out, "launch-notice: an email was sent but could not be recorded; stopping\n")
			printNoticeOutcome(d.Out, c)
			return 1
		}
		c.EmailSent++
		d.Logger.Info("launch-notice: sent", "user_id", t.u.ID.String())
	}
	printNoticeOutcome(d.Out, c)
	if c.EmailFailed > 0 {
		return 1
	}
	return 0
}

func printNoticeOutcome(w io.Writer, c noticeCounts) {
	fmt.Fprintf(w, "done: in-app notices created %d; emails sent %d, failed %d, skipped (claimed elsewhere) %d\n", c.InAppCreated, c.EmailSent, c.EmailFailed, c.EmailSkipped)
}

type noticeTarget struct {
	u         announceUserRow
	st        launchnoticeState
	emailable bool
}

// launchNoticeTest sends the email to the one --test-to account and writes nothing.
func launchNoticeTest(ctx context.Context, d launchNoticeDeps, p launchnotice.Params, targets []noticeTarget) int {
	if len(targets) != 1 || !targets[0].emailable {
		fmt.Fprintln(d.Out, "launch-notice: --test-to user is not an eligible recipient (missing, disabled, unverified, excluded or bad address)")
		return 1
	}
	t := targets[0]
	err := d.Mailer.Send(ctx, email.Message{To: strings.TrimSpace(t.u.Email), Subject: launchnotice.Subject, Text: launchnotice.RenderEmail(p),
		IdempotencyKey: "launch-notice-test-" + t.u.ID.String() + "-" + p.Launch})
	if err != nil {
		d.Logger.Warn("launch-notice: test send failed", "user_id", t.u.ID.String(), "err", logging.ScrubErr(err))
		fmt.Fprintln(d.Out, "launch-notice: test send failed")
		return 1
	}
	fmt.Fprintln(d.Out, "launch-notice: test email sent; nothing was recorded")
	return 0
}
