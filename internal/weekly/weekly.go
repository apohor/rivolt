// Package weekly is the opt-in weekly recap email: on Monday morning, last
// week's miles, energy, cost and efficiency, and lease pace if the user tracks a
// lease. Plain text, no images, no tracking, a signed one-click unsubscribe.
// A week with no drive and no charge sends nothing.
//
// Every send goes through internal/engage (toggle, budget, once per account and
// week), so it is safe on every replica and counts toward the monthly budget.
package weekly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/statement"
)

// Kind is the engage kind of the recap.
const Kind = "weekly_recap"

// The recap goes out on Monday between SendFromHour and SendUntilHour local: a
// few hours' grace so a restart or an outage at 08:00 does not cost the week
// (the claim still makes it once).
const (
	SendFromHour  = 8
	SendUntilHour = 12
	SweepEvery    = 30 * time.Minute
	// BestDriveMinMiles keeps a short hop from being the "best drive".
	BestDriveMinMiles = 5.0
)

// LeasePace is how the user stands against the lease allowance.
type LeasePace struct {
	// MilesOver is positive when they are over pace, negative when under.
	MilesOver float64
}

// Summary is one week's figures.
type Summary struct {
	Model      string
	Currency   string
	Miles      float64
	Drives     int
	KWhUsed    float64 // 0: unknown
	MiPerKWh   float64 // 0: unknown
	KWhCharged float64
	Cost       float64
	// HomePct is the share of charged energy done at home; nil when no home
	// place is set or nothing was charged.
	HomePct *int
	// BestMiles and BestMiPerKWh describe the most efficient drive; zero when the
	// week had fewer than two drives.
	BestMiles, BestMiPerKWh float64
	Lease                   *LeasePace
	// Charges counts charging sessions in the week.
	Charges int
}

// Empty is true for a week with no drive and no charge.
func (s Summary) Empty() bool { return s.Drives == 0 && s.Charges == 0 }

// Week returns the previous full local week (Monday to Monday) before now in loc,
// and its key "weekly:{ISO year}-W{ISO week}".
func Week(now time.Time, loc *time.Location) (key string, start, end time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	l := now.In(loc)
	back := (int(l.Weekday()) + 6) % 7 // days since Monday
	thisMon := time.Date(l.Year(), l.Month(), l.Day()-back, 0, 0, 0, 0, loc)
	start = thisMon.AddDate(0, 0, -7)
	end = thisMon
	y, w := start.ISOWeek()
	return fmt.Sprintf("weekly:%d-W%02d", y, w), start.UTC(), end.UTC()
}

// Due reports whether it is the sending window for a user in loc.
func Due(now time.Time, loc *time.Location) bool {
	l := now.In(loc)
	return l.Weekday() == time.Monday && l.Hour() >= SendFromHour && l.Hour() < SendUntilHour
}

func money(v float64, cur string) string {
	if cur == "" || strings.EqualFold(cur, "USD") {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("%.2f %s", v, strings.ToUpper(cur))
}

func num(v float64) string { return fmt.Sprintf("%.1f", v) }

// Render writes the subject and the plain-text body.
func Render(s Summary, appURL, unsubURL string) (subject, text string) {
	subject = fmt.Sprintf("Your Rivian last week: %s mi, %s", num(s.Miles), money(s.Cost, s.Currency))
	model := strings.TrimSpace(s.Model)
	if model == "" {
		model = "Rivian"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Last week in your %s:\n\n", model)
	row := func(label, value string) { fmt.Fprintf(&b, "  %-17s %s\n", label, value) }
	drives := "1 drive"
	if s.Drives != 1 {
		drives = fmt.Sprintf("%d drives", s.Drives)
	}
	row("Miles driven", fmt.Sprintf("%s mi (%s)", num(s.Miles), drives))
	if s.KWhUsed > 0 && s.MiPerKWh > 0 {
		row("Energy used", fmt.Sprintf("%s kWh, %.2f mi/kWh", num(s.KWhUsed), s.MiPerKWh))
	}
	charging := fmt.Sprintf("%s kWh, %s", num(s.KWhCharged), money(s.Cost, s.Currency))
	if s.HomePct != nil {
		charging += fmt.Sprintf(" (%d%% at home)", *s.HomePct)
	}
	row("Charging", charging)
	if s.BestMiles > 0 && s.BestMiPerKWh > 0 {
		row("Best drive", fmt.Sprintf("%s mi at %.2f mi/kWh", num(s.BestMiles), s.BestMiPerKWh))
	}
	if s.Lease != nil {
		switch m := math.Round(s.Lease.MilesOver); {
		case m > 0:
			row("Lease pace", fmt.Sprintf("%.0f mi over pace for your allowance", m))
		case m < 0:
			row("Lease pace", fmt.Sprintf("%.0f mi under pace", -m))
		default:
			row("Lease pace", "on pace for your allowance")
		}
	}
	b.WriteString("\nCost figures use your own rates where you set them and estimates elsewhere; each charge shows its source in the app.\n\n")
	fmt.Fprintf(&b, "Open Rivolt: %s\n\n--\nStop the weekly recap: %s\n(or in Settings, under Notifications.) Rivolt is unofficial and not affiliated with Rivian.\n", appURL, unsubURL)
	return subject, b.String()
}

// Candidate is an account that turned the recap on.
type Candidate struct {
	User     uuid.UUID
	Email    string
	Timezone string
	Model    string
}

// Source reads who gets the recap and whether they still want it.
type Source interface {
	Candidates(ctx context.Context) ([]Candidate, error)
	// Enabled reads the live toggle.
	Enabled(ctx context.Context, uid uuid.UUID) (bool, error)
}

// Mailer is the part of the email client the job uses.
type Mailer interface {
	Send(ctx context.Context, m email.Message) error
}

// SummaryFunc computes the figures for the local week [start, end).
type SummaryFunc func(ctx context.Context, c Candidate, start, end time.Time, loc *time.Location) (Summary, error)

// Job sends the recaps.
type Job struct {
	Source    Source
	Summarize SummaryFunc
	Mailer    Mailer
	Signer    *statement.Signer
	BaseURL   string
	Engage    *engage.Engage
	Now       func() time.Time
	Logger    *slog.Logger
	// Pause is called between sends (the throttle); nil uses 500 ms.
	Pause func(ctx context.Context)
}

// Stats is what one sweep did.
type Stats struct{ Considered, Due, Sent, Skipped, Failed int }

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Job) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

// Run sweeps shortly after start and then every SweepEvery until ctx ends.
func (j *Job) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute): // let the pod settle first
	}
	for {
		if _, err := j.Sweep(ctx); err != nil && ctx.Err() == nil {
			j.logger().Warn("weekly recap sweep failed", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(SweepEvery):
		}
	}
}

// Sweep considers every opted-in account once.
func (j *Job) Sweep(ctx context.Context) (Stats, error) {
	var st Stats
	cands, err := j.Source.Candidates(ctx)
	if err != nil {
		return st, err
	}
	now := j.now()
	first := true
	for _, c := range cands {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		st.Considered++
		loc, err := time.LoadLocation(strings.TrimSpace(c.Timezone))
		if err != nil {
			loc = time.UTC
		}
		if !Due(now, loc) {
			continue
		}
		st.Due++
		key, start, end := Week(now, loc)
		sum, err := j.Summarize(ctx, c, start, end, loc)
		if err != nil {
			// Never send partial or estimated totals.
			j.logger().Warn("weekly recap: figures unavailable", "week", key, "err", err.Error())
			st.Failed++
			continue
		}
		if sum.Empty() {
			if ok, err := j.Engage.Skip(ctx, c.User, Kind, key, engage.Email, engage.StatusEmpty); err == nil && ok {
				st.Skipped++
			}
			continue
		}
		if !first {
			j.pause(ctx)
		}
		first = false
		uid := c.User
		out, err := j.Engage.Send(ctx, engage.Msg{
			User: uid, Kind: Kind, Key: key, Channel: engage.Email,
			Toggle: func(ctx context.Context) (bool, error) { return j.Source.Enabled(ctx, uid) },
		}, func(ctx context.Context) error { return j.Mailer.Send(ctx, j.message(c, key, sum)) })
		switch out {
		case engage.Sent:
			st.Sent++
		case engage.Failed:
			st.Failed++
			j.logger().Warn("weekly recap: not sent", "week", key)
		}
		_ = err
	}
	return st, nil
}

func (j *Job) pause(ctx context.Context) {
	if j.Pause != nil {
		j.Pause(ctx)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
	}
}

// message renders the email for a candidate and week.
func (j *Job) message(c Candidate, key string, s Summary) email.Message {
	base := strings.TrimRight(j.BaseURL, "/")
	unsub := base + "/api/weekly-recap/unsubscribe?t=" + j.Signer.Sign(c.User, key, j.now().Add(statement.TokenTTL))
	s.Model = firstNonEmpty(s.Model, c.Model)
	subject, text := Render(s, base+"/?src=weekly", unsub)
	return email.Message{
		To: c.Email, Subject: subject, Text: text,
		IdempotencyKey: IdempotencyKey(c.User, key),
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + unsub + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// IdempotencyKey is a stable, opaque per-(account, week) key; the raw user id
// never leaves the server.
func IdempotencyKey(uid uuid.UUID, week string) string {
	sum := sha256.Sum256([]byte("rivolt/weekly/" + uid.String() + "/" + week))
	return "wk-" + hex.EncodeToString(sum[:16])
}
