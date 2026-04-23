package ai

// Usage recording: a tiny append-only ledger of LLM calls the app has
// made, what they cost, and which feature triggered them.
//
// Each provider reports real input/output token counts on its response
// (OpenAI usage.prompt_tokens/completion_tokens, Anthropic
// usage.input_tokens/output_tokens, Gemini usageMetadata.*TokenCount).
// We store those verbatim and multiply by the provider's published
// per-1M-token price to get an actual $ figure. Nothing is estimated.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Recorder persists AI call metadata to SQLite.
type Recorder struct {
	db *sql.DB
}

// NewRecorder wires a Recorder to an already-open *sql.DB (we reuse the
// shots database to avoid a second file).
func NewRecorder(db *sql.DB) (*Recorder, error) {
	if db == nil {
		return nil, fmt.Errorf("recorder: db is nil")
	}
	const schema = `
CREATE TABLE IF NOT EXISTS ai_usage (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    time_unix     INTEGER NOT NULL,
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL,
    feature       TEXT NOT NULL,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL NOT NULL DEFAULT 0,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    shot_id       TEXT,
    ok            INTEGER NOT NULL DEFAULT 1,
    error         TEXT
);
CREATE INDEX IF NOT EXISTS idx_ai_usage_time ON ai_usage(time_unix DESC);
CREATE INDEX IF NOT EXISTS idx_ai_usage_provider ON ai_usage(provider);
`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("usage schema: %w", err)
	}
	// Migration: older installs had input_chars/output_chars columns and
	// cost_usd populated from a chars/4 heuristic. Add the token columns
	// if missing and null out the old estimated costs so the dashboard
	// doesn't mix real $ with guesses. We keep the legacy columns (can't
	// DROP in sqlite without a table rebuild) but stop writing to them.
	addCol := func(col, decl string) {
		var cnt int
		_ = db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('ai_usage') WHERE name=?`,
			col,
		).Scan(&cnt)
		if cnt == 0 {
			if _, err := db.Exec(
				`ALTER TABLE ai_usage ADD COLUMN ` + col + ` ` + decl); err != nil {
				slog.Warn("ai_usage add column failed", "col", col, "err", err.Error())
			}
		}
	}
	addCol("input_tokens", "INTEGER NOT NULL DEFAULT 0")
	addCol("output_tokens", "INTEGER NOT NULL DEFAULT 0")
	// Legacy rows (no tokens recorded) had a char-based "estimated" cost
	// that we no longer want showing up in totals. Zero their cost once.
	var legacy int
	_ = db.QueryRow(
		`SELECT COUNT(*) FROM ai_usage WHERE input_tokens=0 AND output_tokens=0 AND cost_usd>0`,
	).Scan(&legacy)
	if legacy > 0 {
		if _, err := db.Exec(
			`UPDATE ai_usage SET cost_usd=0 WHERE input_tokens=0 AND output_tokens=0`); err != nil {
			slog.Warn("ai_usage legacy zero-out failed", "err", err.Error())
		} else {
			slog.Info("ai_usage: zeroed legacy estimated costs", "rows", legacy)
		}
	}
	return &Recorder{db: db}, nil
}

// Record is the event we log for each LLM call.
type Record struct {
	Time         time.Time
	Provider     string // openai, anthropic, gemini
	Model        string // gpt-4o-mini, claude-haiku-4-5, ...
	Feature      string // analyze, coach, compare, digest, ask, name, transcribe, image
	InputTokens  int64
	OutputTokens int64
	DurationMs   int64
	ShotID       string
	OK           bool
	Err          string
}

// Record stores a single call. Failures in the recorder are logged but
// never bubble up — we don't want telemetry to break user-facing flows.
func (r *Recorder) Record(ctx context.Context, rec Record) {
	if r == nil || r.db == nil {
		return
	}
	cost := ComputeCost(rec.Provider, rec.Model, rec.InputTokens, rec.OutputTokens)
	var shotID any
	if rec.ShotID != "" {
		shotID = rec.ShotID
	}
	okVal := 1
	if !rec.OK {
		okVal = 0
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO ai_usage (time_unix, provider, model, feature, input_tokens, output_tokens, cost_usd, duration_ms, shot_id, ok, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Time.Unix(), rec.Provider, rec.Model, rec.Feature,
		rec.InputTokens, rec.OutputTokens, cost, rec.DurationMs,
		shotID, okVal, rec.Err)
	if err != nil {
		slog.Warn("ai usage record failed", "err", err.Error())
	}
}

// UsageSummary aggregates recent activity for the dashboard.
type UsageSummary struct {
	Since        time.Time            `json:"since"`
	TotalCalls   int                  `json:"total_calls"`
	TotalCostUSD float64              `json:"total_cost_usd"`
	ByProvider   map[string]CostBreak `json:"by_provider"`
	ByFeature    map[string]CostBreak `json:"by_feature"`
	ByModel      map[string]CostBreak `json:"by_model"`
	Recent       []Record             `json:"recent"` // newest first
}

// CostBreak is the per-slice rollup shown in the dashboard.
type CostBreak struct {
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	LastUsedUnix int64   `json:"last_used_unix,omitempty"`
}

// Summarize returns rollups for the last `days` days plus the N most
// recent raw records. `days<=0` means "all time".
func (r *Recorder) Summarize(ctx context.Context, days, recent int) (*UsageSummary, error) {
	if r == nil || r.db == nil {
		return &UsageSummary{}, nil
	}
	if recent <= 0 {
		recent = 50
	}
	var since time.Time
	args := []any{}
	where := ""
	if days > 0 {
		since = time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		where = "WHERE time_unix >= ?"
		args = append(args, since.Unix())
	}

	sum := &UsageSummary{
		Since:      since,
		ByProvider: map[string]CostBreak{},
		ByFeature:  map[string]CostBreak{},
		ByModel:    map[string]CostBreak{},
	}

	row := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(cost_usd),0) FROM ai_usage `+where, args...)
	if err := row.Scan(&sum.TotalCalls, &sum.TotalCostUSD); err != nil {
		return nil, err
	}

	for _, spec := range []struct {
		col string
		dst map[string]CostBreak
	}{
		{"provider", sum.ByProvider},
		{"feature", sum.ByFeature},
		{"model", sum.ByModel},
	} {
		q := `SELECT ` + spec.col + `,
		         COUNT(*),
		         COALESCE(SUM(input_tokens),0),
		         COALESCE(SUM(output_tokens),0),
		         COALESCE(SUM(cost_usd),0),
		         COALESCE(MAX(time_unix),0)
		      FROM ai_usage ` + where + `
		      GROUP BY ` + spec.col + `
		      ORDER BY SUM(cost_usd) DESC`
		rows, err := r.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var c int
			var inTok, outTok, last int64
			var cost float64
			if err := rows.Scan(&k, &c, &inTok, &outTok, &cost, &last); err != nil {
				rows.Close()
				return nil, err
			}
			spec.dst[k] = CostBreak{
				Calls:        c,
				InputTokens:  inTok,
				OutputTokens: outTok,
				CostUSD:      cost,
				LastUsedUnix: last,
			}
		}
		rows.Close()
	}

	// Recent calls.
	recQ := `SELECT time_unix, provider, model, feature, input_tokens, output_tokens, cost_usd,
		    duration_ms, COALESCE(shot_id,''), ok, COALESCE(error,'')
		FROM ai_usage ` + where + ` ORDER BY time_unix DESC LIMIT ?`
	recArgs := append([]any{}, args...)
	recArgs = append(recArgs, recent)
	rows, err := r.db.QueryContext(ctx, recQ, recArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ts                   int64
			prov, mod, feat, sh  string
			inTok, outTok, durMs int64
			cost                 float64
			okI                  int
			errS                 string
		)
		if err := rows.Scan(&ts, &prov, &mod, &feat, &inTok, &outTok, &cost, &durMs, &sh, &okI, &errS); err != nil {
			return nil, err
		}
		_ = cost
		sum.Recent = append(sum.Recent, Record{
			Time:         time.Unix(ts, 0).UTC(),
			Provider:     prov,
			Model:        mod,
			Feature:      feat,
			InputTokens:  inTok,
			OutputTokens: outTok,
			DurationMs:   durMs,
			ShotID:       sh,
			OK:           okI == 1,
			Err:          errS,
		})
	}
	return sum, rows.Err()
}

// --- pricing ---------------------------------------------------------------

// pricePer1MTokens is the USD price table: (input, output) per 1M tokens.
// Values come from public pricing pages.
var pricePer1MTokens = map[string]struct{ In, Out float64 }{
	// Anthropic
	"anthropic:claude-haiku-4-5":           {1.0, 5.0},
	"anthropic:claude-haiku-4-5-20251001":  {1.0, 5.0},
	"anthropic:claude-sonnet-4-5":          {3.0, 15.0},
	"anthropic:claude-sonnet-4-5-20250929": {3.0, 15.0},
	"anthropic:claude-opus-4-1":            {15.0, 75.0},
	// OpenAI
	"openai:gpt-4o-mini":  {0.15, 0.60},
	"openai:gpt-4o":       {2.50, 10.0},
	"openai:gpt-4.1-mini": {0.40, 1.60},
	"openai:gpt-4.1":      {2.00, 8.00},
	"openai:o4-mini":      {1.10, 4.40},
	"openai:whisper-1":    {0.006, 0},   // per minute, not token — see note below
	"openai:gpt-image-1":  {5.00, 40.0}, // tokens in/out for image model; also has per-image fee
	// Gemini
	"gemini:gemini-2.5-flash":         {0.30, 2.50},
	"gemini:gemini-2.5-flash-preview": {0.30, 2.50},
	"gemini:gemini-2.5-pro":           {1.25, 10.0},
	"gemini:gemini-pro-latest":        {1.25, 10.0},
	"gemini:gemini-2.0-flash":         {0.10, 0.40},
}

// ComputeCost returns the USD cost of a call using real token counts and
// the provider's published per-1M-token price. Unknown models fall back
// to a conservative rate so the dashboard shows *something*.
func ComputeCost(provider, model string, inTokens, outTokens int64) float64 {
	if inTokens <= 0 && outTokens <= 0 {
		return 0
	}
	key := provider + ":" + stripDate(model)
	prices, ok := pricePer1MTokens[key]
	if !ok {
		prices, ok = pricePer1MTokens[provider+":"+model]
	}
	if !ok {
		prices = struct{ In, Out float64 }{1.0, 4.0}
	}
	return (float64(inTokens)*prices.In + float64(outTokens)*prices.Out) / 1_000_000.0
}

// stripDate removes trailing date suffixes like "-20251001" so the same
// model family maps to a single price entry.
func stripDate(model string) string {
	// Claude models use "name-YYYYMMDD" at the end.
	if idx := strings.LastIndex(model, "-"); idx > 0 && idx < len(model)-1 {
		tail := model[idx+1:]
		if len(tail) == 8 && isAllDigits(tail) {
			return model[:idx]
		}
	}
	return model
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// SplitModelName decomposes a Provider.Name() result (e.g. "anthropic:claude-haiku-4-5")
// into provider and model. Unknown shapes return (name, "").
func SplitModelName(name string) (provider, model string) {
	if i := strings.Index(name, ":"); i > 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}
