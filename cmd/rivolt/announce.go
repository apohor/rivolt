package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/logging"
)

// announceMailer is the slice of *email.Client announce uses, so tests
// can send through a fake.
type announceMailer interface {
	Send(ctx context.Context, m email.Message) error
}

// announceUser is one account the announcement could go to. The address
// is held only to hand to the mailer; nothing prints or logs it.
type announceUser struct {
	ID    uuid.UUID
	Email string
}

// announceSkips counts users left out, by reason, for the dry-run report.
type announceSkips struct {
	Disabled, Unverified, BadAddress, Bypass, Excluded, AlreadySent int
}

// repeatedFlag collects a repeatable string flag.
type repeatedFlag []string

func (r *repeatedFlag) String() string     { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error { *r = append(*r, v); return nil }

// runAnnounce sends a one-time service-update email to every active
// user through the configured Resend mailer (the same client the setup
// nudge and signup mail use). It runs inside a prod pod, so it picks up
// that pod's mailer and secrets; nothing is exported.
//
// Dry run by default: prints the recipient count and user_id prefixes,
// never addresses. --apply sends one message per user (no BCC) at
// --rate per second; --sent-log FILE records each user_id once sent, so a
// re-run after a crash skips them. --test-to USER_ID sends to that one
// user and touches no sent log. Recipients are active users with a
// verified, well-formed address: not disabled, not the auth-bypass or
// demo user, not --exclude-user. The signup-request waitlist is never
// read. Logs carry user ids only; mailer errors are scrubbed of
// addresses.
func runAnnounce(args []string) int {
	fs := flag.NewFlagSet("announce", flag.ContinueOnError)
	subject := fs.String("subject", "", "email subject (required)")
	bodyFile := fs.String("body-file", "", "plain-text body file (required)")
	apply := fs.Bool("apply", false, "send (default: dry run)")
	sentLog := fs.String("sent-log", "", "file of user ids already sent (one per line); appended to as each send succeeds")
	testTo := fs.String("test-to", "", "send only to this user id, ignoring --sent-log")
	rate := fs.Float64("rate", 2, "messages per second")
	var exclude repeatedFlag
	fs.Var(&exclude, "exclude-user", "user id or username to leave out (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*subject) == "" || *bodyFile == "" {
		fmt.Fprintln(os.Stderr, "announce: --subject and --body-file are required")
		return 2
	}
	if *rate <= 0 || *rate > 10 {
		fmt.Fprintln(os.Stderr, "announce: --rate must be in (0, 10]")
		return 2
	}
	raw, err := os.ReadFile(*bodyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "announce: body file:", err)
		return 1
	}
	body := string(raw)
	if err := checkAnnounceBody(body); err != nil {
		fmt.Fprintln(os.Stderr, "announce:", err)
		return 2
	}
	only := uuid.Nil
	if *testTo != "" {
		if only, err = uuid.Parse(strings.TrimSpace(*testTo)); err != nil {
			fmt.Fprintln(os.Stderr, "announce: --test-to must be a user id (uuid)")
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
		fmt.Fprintln(os.Stderr, "announce: postgres:", err)
		return 1
	}
	defer pool.Close()
	if err := db.RequireTables(ctx, pool, "users"); err != nil {
		fmt.Fprintln(os.Stderr, "announce:", err)
		return 1
	}
	var mailer announceMailer
	if *apply || only != uuid.Nil {
		c := email.New(email.Config{APIKey: os.Getenv("RIVOLT_RESEND_API_KEY"), From: os.Getenv("RIVOLT_EMAIL_FROM")})
		if c == nil {
			fmt.Fprintln(os.Stderr, "announce: mailer not configured (RIVOLT_RESEND_API_KEY / RIVOLT_EMAIL_FROM)")
			return 1
		}
		mailer = c
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	return announceRun(ctx, announceDeps{
		List:     func(ctx context.Context) ([]announceUserRow, error) { return listAnnounceUsers(ctx, pool) },
		Mailer:   mailer,
		Out:      os.Stdout,
		Logger:   logger,
		Sleep:    time.Sleep,
		SkipIDs:  skipIDs,
		Excluded: excluded,
		Only:     only,
		Apply:    *apply,
		SentLog:  *sentLog,
		Subject:  strings.TrimSpace(*subject),
		Body:     body,
		Interval: time.Duration(float64(time.Second) / *rate),
	})
}

// checkAnnounceBody refuses a draft that still has an unfilled
// placeholder.
func checkAnnounceBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("body file is empty")
	}
	for _, marker := range []string{"[DECIDE", "TODO", "TBD", "XXX"} {
		if strings.Contains(body, marker) {
			return fmt.Errorf("body still has an unfilled placeholder (%s)", marker)
		}
	}
	return nil
}

// announceUserRow is a users row as the recipient filter sees it.
type announceUserRow struct {
	ID       uuid.UUID
	Email    string
	Disabled bool
	Verified bool
}

func listAnnounceUsers(ctx context.Context, pool *sql.DB) ([]announceUserRow, error) {
	rows, err := pool.QueryContext(ctx, `
		SELECT id, COALESCE(email, ''), disabled, email_verified
		  FROM users
		 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []announceUserRow
	for rows.Next() {
		var u announceUserRow
		if err := rows.Scan(&u.ID, &u.Email, &u.Disabled, &u.Verified); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

type announceDeps struct {
	List     func(context.Context) ([]announceUserRow, error)
	Mailer   announceMailer
	Out      io.Writer
	Logger   *slog.Logger
	Sleep    func(time.Duration)
	SkipIDs  map[uuid.UUID]bool // bypass + demo users
	Excluded map[uuid.UUID]bool // --exclude-user
	Only     uuid.UUID          // --test-to
	Apply    bool
	SentLog  string
	Subject  string
	Body     string
	Interval time.Duration
}

// announceRecipients applies the eligibility rules and the sent log.
func announceRecipients(rows []announceUserRow, d announceDeps, sent map[uuid.UUID]bool) ([]announceUser, announceSkips) {
	var out []announceUser
	var sk announceSkips
	for _, u := range rows {
		if d.Only != uuid.Nil && u.ID != d.Only {
			continue
		}
		switch {
		case d.SkipIDs[u.ID]:
			sk.Bypass++
		case d.Excluded[u.ID]:
			sk.Excluded++
		case u.Disabled:
			sk.Disabled++
		case !u.Verified:
			sk.Unverified++
		case !deliverable(u.Email):
			sk.BadAddress++
		case d.Only == uuid.Nil && sent[u.ID]:
			sk.AlreadySent++
		default:
			out = append(out, announceUser{ID: u.ID, Email: strings.TrimSpace(u.Email)})
		}
	}
	return out, sk
}

// deliverable reports whether s is one bare, well-formed address.
func deliverable(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(a.Address, "@")
}

func announceRun(ctx context.Context, d announceDeps) int {
	sent, err := readSentLog(d.SentLog)
	if err != nil {
		fmt.Fprintln(os.Stderr, "announce: sent log:", err)
		return 1
	}
	rows, err := d.List(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "announce: users:", err)
		return 1
	}
	recs, sk := announceRecipients(rows, d, sent)
	send := d.Apply || d.Only != uuid.Nil
	if d.Only != uuid.Nil && len(recs) == 0 {
		fmt.Fprintln(d.Out, "announce: --test-to user is not an eligible recipient (missing, disabled, unverified, excluded or bad address)")
		return 1
	}

	mode := "dry run"
	if send {
		mode = "sending"
	}
	fmt.Fprintf(d.Out, "announce (%s): subject %q, %d recipient(s)\n", mode, d.Subject, len(recs))
	fmt.Fprintf(d.Out, "left out: disabled %d, unverified %d, bad address %d, bypass/demo %d, excluded %d, already sent %d\n",
		sk.Disabled, sk.Unverified, sk.BadAddress, sk.Bypass, sk.Excluded, sk.AlreadySent)
	if !send {
		for _, r := range recs {
			fmt.Fprintf(d.Out, "  %s\n", r.ID.String()[:8])
		}
		fmt.Fprintln(d.Out, "dry run; pass --apply to send (or --test-to USER_ID for one test send)")
		return 0
	}

	var logFile *os.File
	if d.SentLog != "" && d.Only == uuid.Nil {
		if logFile, err = os.OpenFile(d.SentLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "announce: sent log:", err)
			return 1
		}
		defer logFile.Close()
	}
	sum := sha256.Sum256([]byte(d.Subject + "\x00" + d.Body))
	campaign := hex.EncodeToString(sum[:])[:12]

	okN, failN := 0, 0
	for i, r := range recs {
		if i > 0 && d.Sleep != nil {
			d.Sleep(d.Interval)
		}
		err := d.Mailer.Send(ctx, email.Message{
			To: r.Email, Subject: d.Subject, Text: d.Body,
			// A retried send of the same campaign to the same user is
			// not delivered twice (Resend dedupes within 24 h).
			IdempotencyKey: "announce-" + campaign + "-" + r.ID.String(),
		})
		if err != nil {
			failN++
			d.Logger.Warn("announce: send failed", "user_id", r.ID.String(), "err", logging.ScrubErr(err))
			continue
		}
		okN++
		d.Logger.Info("announce: sent", "user_id", r.ID.String())
		if logFile != nil {
			if _, err := fmt.Fprintln(logFile, r.ID.String()); err != nil {
				fmt.Fprintln(os.Stderr, "announce: sent log write failed; stopping so nobody is sent twice:", err)
				return 1
			}
			_ = logFile.Sync()
		}
	}
	fmt.Fprintf(d.Out, "done: sent %d, failed %d\n", okN, failN)
	if failN > 0 {
		return 1
	}
	return 0
}

// readSentLog loads the user ids already sent; a missing file is empty.
func readSentLog(path string) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		id, err := uuid.Parse(line)
		if err != nil {
			return nil, fmt.Errorf("not a user id: %q", line[:min(len(line), 8)])
		}
		out[id] = true
	}
	return out, sc.Err()
}
