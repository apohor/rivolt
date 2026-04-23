// AI shot analysis panel: button to run, cached summary rendering, metrics.
//
// On mount we try GET /api/shots/:id/analysis for a cached result. A
// "Run analysis" button fires POST to create a fresh one. Posting again
// overwrites the cache (useful if you tweak the prompt or switch models).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import ReactMarkdown, { type Components } from "react-markdown";
import { backend, MachineError, type ShotAnalysis } from "../lib/api";
import {
  parseAnalysis,
  type ParsedObservation,
  type RecipeChange,
} from "../lib/analysisSections";
import { Card, ErrorBox } from "./ui";
import ApplyProfileChange from "./ApplyProfileChange";

type Props = {
  shotId: string;
  /** Profile id this shot was pulled with. Enables one-click "Apply" on
   *  recipe changes. Omit for live view where the shot may not yet be
   *  linked to an editable profile. */
  profileId?: string;
  /**
   * When true, auto-poll the backend for a cached analysis every few
   * seconds and show a "waiting" state until it arrives. Used by the
   * Live page where the server-side auto-analyze trigger is writing
   * the result in the background — users shouldn't have to click.
   */
  autoPoll?: boolean;
};

export default function ShotAnalysisPanel({ shotId, profileId, autoPoll }: Props) {
  const qc = useQueryClient();

  // Fetch cached analysis; 404 is an expected "not yet analyzed" state.
  const cached = useQuery({
    queryKey: ["analysis", shotId],
    queryFn: async () => {
      try {
        return await backend.getAnalysis(shotId);
      } catch (e) {
        if (e instanceof MachineError && e.status === 404) return null;
        throw e;
      }
    },
    enabled: !!shotId,
    // While autoPoll is set and we don't have a result yet, re-check
    // every 3s. Auto-analyze usually lands within 10-30s of shot end
    // (fast models) or up to a couple of minutes (reasoning models).
    refetchInterval: (q) => (autoPoll && !q.state.data ? 3000 : false),
  });

  const run = useMutation({
    mutationFn: () => backend.createAnalysis(shotId),
    onSuccess: (data) => {
      qc.setQueryData<ShotAnalysis | null>(["analysis", shotId], data);
    },
  });

  const analysis: ShotAnalysis | null | undefined = run.data ?? cached.data;
  const isAIDisabled =
    (cached.error instanceof MachineError && cached.error.status === 503) ||
    (run.error instanceof MachineError && run.error.status === 503);

  return (
    <Card
      title="AI analysis"
      actions={
        analysis ? (
          <button
            onClick={() => run.mutate()}
            disabled={run.isPending}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-xs hover:bg-neutral-800 disabled:opacity-50"
          >
            {run.isPending ? "Re-running…" : "Re-run"}
          </button>
        ) : null
      }
    >
      {isAIDisabled ? (
        <p className="text-sm text-neutral-400">
          AI analysis is not configured.{" "}
          <a href="/settings" className="text-emerald-400 underline hover:text-emerald-300">
            Add a provider and API key under Settings
          </a>{" "}
          to enable it.
        </p>
      ) : cached.isLoading ? (
        <p className="text-sm text-neutral-500">Checking for a previous analysis…</p>
      ) : !analysis ? (
        autoPoll ? (
          <div className="flex items-center gap-3 text-sm text-neutral-400">
            <span
              className="inline-block h-3 w-3 animate-spin rounded-full border-2 border-neutral-700 border-t-emerald-400"
              aria-hidden
            />
            <span>Analyzing shot… the server is calling the model in the background.</span>
          </div>
        ) : (
          <div className="space-y-3">
            <p className="text-sm text-neutral-400">
              Get a critique of this shot — pressure, flow, weight, and profile
              adherence — from an LLM. Typically completes in under 15 seconds.
            </p>
            <button
              onClick={() => run.mutate()}
              disabled={run.isPending}
              className="rounded-md border border-emerald-800 bg-emerald-900/40 px-3 py-1.5 text-sm text-emerald-200 hover:bg-emerald-900/60 disabled:opacity-50"
            >
              {run.isPending ? "Analyzing…" : "Run analysis"}
            </button>
            {run.error && !(run.error instanceof MachineError && run.error.status === 503) && (
              <ErrorBox title="Analysis failed" detail={String(run.error)} />
            )}
          </div>
        )
      ) : (
        <AnalysisBody analysis={analysis} profileId={profileId} />
      )}
    </Card>
  );
}

// AnalysisBody renders a parsed analysis as scannable sections:
//   1. Hero — rating badge + TL;DR paragraph, side-by-side.
//   2. Observations (left) + Preparation / Recipe-changes (right).
//   3. Footer — computed metrics (collapsed) + model/timestamp.
// If the markdown doesn't parse into recognisable sections we fall back
// to rendering the raw ReactMarkdown so we never lose information.
function AnalysisBody({
  analysis,
  profileId,
}: {
  analysis: ShotAnalysis;
  profileId?: string;
}) {
  const parsed = parseAnalysis(analysis.summary);
  const m = analysis.metrics;
  const hasActions =
    parsed.preparation.length > 0 ||
    parsed.recipe.length > 0 ||
    parsed.suggestions.length > 0;
  return (
    <div className="space-y-5">
      {/* Hero: rating + tl;dr */}
      <div className="flex flex-col gap-4 md:flex-row md:items-stretch">
        {analysis.rating ? (
          <RatingBadge rating={analysis.rating} />
        ) : null}
        {parsed.tldr ? (
          <div className="flex-1 rounded-lg border border-neutral-800 bg-neutral-900/40 p-4 text-[15px] leading-relaxed text-neutral-200">
            <ReactMarkdown components={markdownComponents}>{parsed.tldr}</ReactMarkdown>
          </div>
        ) : null}
      </div>

      {/* Observations + action columns */}
      {parsed.ok ? (
        <div className="grid gap-4 md:grid-cols-2">
          {parsed.observations.length > 0 && (
            <SectionCard title="What the numbers say" accent="sky">
              <ul className="space-y-3">
                {(() => {
                  const displayLabels = disambiguateLabels(
                    parsed.observations.map((o) => o.label),
                  );
                  return parsed.observations.map((o, i) => (
                    <ObservationRow
                      key={i}
                      obs={o}
                      displayLabel={displayLabels[i]}
                    />
                  ));
                })()}
              </ul>
            </SectionCard>
          )}
          {hasActions && (
            <div className="space-y-4">
              {parsed.preparation.length > 0 && (
                <SectionCard title="Preparation" accent="amber">
                  <ol className="space-y-3">
                    {parsed.preparation.map((s, i) => (
                      <SuggestionRow key={i} n={i + 1} body={s} tone="amber" />
                    ))}
                  </ol>
                </SectionCard>
              )}
              {parsed.recipe.length > 0 && (
                <SectionCard title="Recipe changes" accent="emerald">
                  <ol className="space-y-3">
                    {parsed.recipe.map((r, i) => (
                      <RecipeRow key={i} n={i + 1} change={r} profileId={profileId} />
                    ))}
                  </ol>
                </SectionCard>
              )}
              {/* Legacy flat Suggestions (older cached analyses). */}
              {parsed.suggestions.length > 0 && (
                <SectionCard title="Suggestions" accent="emerald">
                  <ol className="space-y-3">
                    {parsed.suggestions.map((s, i) => (
                      <SuggestionRow key={i} n={i + 1} body={s} tone="emerald" />
                    ))}
                  </ol>
                </SectionCard>
              )}
            </div>
          )}
        </div>
      ) : (
        // Fallback: render the summary verbatim so nothing is lost when
        // the model deviates from the expected section shape.
        <div className="rounded-md border border-neutral-800 bg-neutral-950/40 p-4 text-[15px] leading-relaxed text-neutral-200">
          <ReactMarkdown components={markdownComponents}>{analysis.summary}</ReactMarkdown>
        </div>
      )}

      {/* Metrics */}
      <details className="rounded-md border border-neutral-800 bg-neutral-900/30 open:bg-neutral-900/40">
        <summary className="cursor-pointer select-none px-4 py-2 text-xs font-medium uppercase tracking-wide text-neutral-400 hover:text-neutral-200">
          Computed metrics
        </summary>
        <dl className="grid grid-cols-2 gap-x-4 gap-y-3 px-4 pb-4 sm:grid-cols-4">
          <Metric k="Duration" v={`${m.duration_s.toFixed(1)} s`} />
          <Metric k="Final weight" v={`${m.final_weight_g.toFixed(1)} g`} />
          <Metric k="Peak pressure" v={`${m.peak_pressure_bar.toFixed(2)} bar`} />
          <Metric k="Avg pressure" v={`${m.avg_pressure_bar.toFixed(2)} bar`} />
          <Metric k="Peak flow" v={`${m.peak_flow_mls.toFixed(2)} ml/s`} />
          <Metric k="Avg flow" v={`${m.avg_flow_mls.toFixed(2)} ml/s`} />
          {m.first_drip_s != null && (
            <Metric k="First drip" v={`${m.first_drip_s.toFixed(1)} s`} />
          )}
          {m.preinfusion_end_s != null && (
            <Metric k="Preinfusion end" v={`${m.preinfusion_end_s.toFixed(1)} s`} />
          )}
        </dl>
      </details>

      {/* Provenance */}
      <footer className="flex flex-wrap items-center justify-between gap-2 border-t border-neutral-900 pt-3 text-[11px] text-neutral-500">
        <span>
          model:{" "}
          <code className="rounded bg-neutral-900 px-1.5 py-0.5 font-mono text-neutral-300">
            {analysis.model}
          </code>
        </span>
        <span>generated {new Date(analysis.created_at).toLocaleString()}</span>
      </footer>
    </div>
  );
}

// markdownComponents gives the LLM output explicit, readable typography
// without pulling in @tailwindcss/typography. We target the shapes Claude,
// GPT, and Gemini actually produce: short paragraphs, H2/H3 section
// headings, bullet and numbered lists, bold labels, inline code for metrics.
const markdownComponents: Components = {
  h1: (p) => <h3 className="mt-4 mb-2 text-base font-semibold text-neutral-100" {...p} />,
  h2: (p) => <h3 className="mt-4 mb-2 text-base font-semibold text-neutral-100" {...p} />,
  h3: (p) => <h4 className="mt-3 mb-1.5 text-sm font-semibold uppercase tracking-wide text-neutral-400" {...p} />,
  h4: (p) => <h4 className="mt-3 mb-1.5 text-sm font-semibold uppercase tracking-wide text-neutral-400" {...p} />,
  p: (p) => <p className="my-3 first:mt-0 last:mb-0" {...p} />,
  ul: (p) => <ul className="my-3 space-y-1.5 pl-5 [&_li]:list-disc marker:text-neutral-600" {...p} />,
  ol: (p) => <ol className="my-3 space-y-1.5 pl-5 [&_li]:list-decimal marker:text-neutral-600" {...p} />,
  li: (p) => <li className="pl-1 [&>p]:my-1" {...p} />,
  strong: (p) => <strong className="font-semibold text-neutral-50" {...p} />,
  em: (p) => <em className="text-neutral-300" {...p} />,
  code: (p) => (
    <code className="rounded bg-neutral-800/70 px-1 py-0.5 font-mono text-[13px] text-amber-200" {...p} />
  ),
  blockquote: (p) => (
    <blockquote
      className="my-3 border-l-2 border-emerald-800 pl-3 italic text-neutral-300"
      {...p}
    />
  ),
  hr: () => <hr className="my-4 border-neutral-800" />,
  a: (p) => <a className="text-emerald-400 underline hover:text-emerald-300" {...p} />,
};

function Metric({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-md border border-neutral-800 bg-neutral-900/40 px-3 py-2">
      <dt className="text-[10px] uppercase tracking-wide text-neutral-500">{k}</dt>
      <dd className="font-mono text-sm text-neutral-100">{v}</dd>
    </div>
  );
}

// SectionCard is the scannable wrapper around Observations / Suggestions.
// We keep the chrome light so the content carries the eye.
function SectionCard({
  title,
  accent,
  children,
}: {
  title: string;
  accent: "sky" | "emerald" | "amber";
  children: React.ReactNode;
}) {
  const bar =
    accent === "sky"
      ? "bg-sky-600"
      : accent === "amber"
        ? "bg-amber-600"
        : "bg-emerald-600";
  return (
    <section className="overflow-hidden rounded-lg border border-neutral-800 bg-neutral-900/30">
      <header className="flex items-center gap-2 border-b border-neutral-800 px-4 py-2.5">
        <span className={`h-4 w-1 rounded-full ${bar}`} aria-hidden />
        <h3 className="text-xs font-semibold uppercase tracking-wide text-neutral-300">
          {title}
        </h3>
      </header>
      <div className="px-4 py-3 text-[14px] leading-relaxed text-neutral-200">
        {children}
      </div>
    </section>
  );
}

// ObservationRow renders a single "what the numbers say" bullet as a
// label pill + body. Label colour is derived from the label text so that
// Pressure / Flow / Weight etc. get consistent hues across shots. The
// pill column is fixed-width so bodies of different-length labels
// ("Pressure" vs "Preinfusion") align to the same left edge.
//
// Multi-word labels from the LLM ("Pressure Decline Collapse") are
// compacted to their dominant keyword via shortLabel() so the pill
// stays single-line. When two observations would collapse to the
// SAME keyword we pass a disambiguated `displayLabel` ("Pressure ·
// Decline") so the pills don't look like duplicates.
function ObservationRow({
  obs,
  displayLabel,
}: {
  obs: ParsedObservation;
  displayLabel?: string;
}) {
  const tone = labelTone(obs.label);
  const text = displayLabel ?? shortLabel(obs.label);
  return (
    <li className="flex flex-col gap-1.5 sm:grid sm:grid-cols-[8rem_1fr] sm:gap-3">
      {obs.label ? (
        <span
          className={`inline-flex h-fit w-fit items-center rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${tone}`}
          title={obs.label}
        >
          {text}
        </span>
      ) : (
        <span aria-hidden />
      )}
      <span className="min-w-0">
        <ReactMarkdown components={inlineMarkdownComponents}>
          {obs.body || "—"}
        </ReactMarkdown>
      </span>
    </li>
  );
}

// disambiguateLabels resolves short-label collisions by appending the
// first non-keyword word from the original label. "Pressure" +
// "Pressure Decline Collapse" → ["Pressure", "Pressure · Decline"].
// Labels that don't collide are returned unchanged.
function disambiguateLabels(labels: string[]): string[] {
  const shorts = labels.map(shortLabel);
  const counts = new Map<string, number>();
  for (const s of shorts) counts.set(s, (counts.get(s) ?? 0) + 1);
  return labels.map((full, i) => {
    const s = shorts[i];
    if ((counts.get(s) ?? 0) < 2) return s;
    // Find the first word in `full` that's not the dominant keyword.
    const extra = full
      .split(/\s+/)
      .find((w) => w && w.toLowerCase() !== s.toLowerCase());
    if (!extra) return s;
    const cap = extra.charAt(0).toUpperCase() + extra.slice(1).toLowerCase();
    return `${s} · ${cap}`;
  });
}

// shortLabel picks a single dominant keyword out of a multi-word LLM
// label so the fixed-width pill stays on one line. Order is priority:
// first keyword that matches wins. Unknown labels fall back to the
// first word, title-cased.
function shortLabel(label: string): string {
  const l = label.toLowerCase();
  const keywords: [RegExp, string][] = [
    [/pressure/, "Pressure"],
    [/preinfus/, "Preinfusion"],
    [/infus/, "Infusion"],
    [/flow/, "Flow"],
    [/yield/, "Yield"],
    [/weight|mass/, "Weight"],
    [/dose/, "Dose"],
    [/temp|heat/, "Temp"],
    [/grind/, "Grind"],
    [/puck/, "Puck"],
    [/time|duration|timing/, "Time"],
    [/extract/, "Extraction"],
  ];
  for (const [re, out] of keywords) if (re.test(l)) return out;
  const first = label.split(/\s+/)[0] ?? label;
  return first.charAt(0).toUpperCase() + first.slice(1).toLowerCase();
}

// SuggestionRow renders a numbered suggestion with a circled index on
// the left so the action list scans at a glance. Tone colours the index
// circle so Preparation (amber) and legacy Suggestions (emerald) stay
// visually distinct.
function SuggestionRow({
  n,
  body,
  tone,
}: {
  n: number;
  body: string;
  tone: "amber" | "emerald";
}) {
  const chip =
    tone === "amber"
      ? "border-amber-700 bg-amber-900/40 text-amber-200"
      : "border-emerald-700 bg-emerald-900/40 text-emerald-200";
  return (
    <li className="flex items-start gap-3">
      <span
        className={`mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full border text-[11px] font-semibold ${chip}`}
      >
        {n}
      </span>
      <span className="flex-1">
        <ReactMarkdown components={inlineMarkdownComponents}>{body}</ReactMarkdown>
      </span>
    </li>
  );
}

// RecipeRow renders a recipe-change bullet. If the LLM emitted a
// `SET variable <key> = <value>` directive we surface an "Apply" button
// that fetches the profile, updates that variable, and POSTs it back to
// the machine. When the directive is absent we fall back to showing the
// prose only (still counted as a recipe change).
function RecipeRow({
  n,
  change,
  profileId,
}: {
  n: number;
  change: RecipeChange;
  profileId?: string;
}) {
  return (
    <li className="flex items-start gap-3">
      <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-emerald-700 bg-emerald-900/40 text-[11px] font-semibold text-emerald-200">
        {n}
      </span>
      <div className="flex-1 space-y-2">
        {change.body && (
          <ReactMarkdown components={inlineMarkdownComponents}>{change.body}</ReactMarkdown>
        )}
        {change.variableKey != null && change.value != null && (
          <ApplyProfileChange
            profileId={profileId}
            action={{
              kind: "set_variable",
              variableKey: change.variableKey,
              value: change.value,
            }}
          />
        )}
        {change.stageOp && (
          <ApplyProfileChange profileId={profileId} action={change.stageOp} />
        )}
      </div>
    </li>
  );
}

// inlineMarkdownComponents unwraps react-markdown's default <p> so bullet
// bodies render inline next to labels / numbers without introducing an
// unwanted block-level break.
const inlineMarkdownComponents: Components = {
  p: ({ children }) => <>{children}</>,
  strong: (p) => <strong className="font-semibold text-neutral-50" {...p} />,
  em: (p) => <em className="text-neutral-300" {...p} />,
  code: (p) => (
    <code
      className="rounded bg-neutral-800/70 px-1 py-0.5 font-mono text-[12px] text-amber-200"
      {...p}
    />
  ),
  a: (p) => <a className="text-emerald-400 underline hover:text-emerald-300" {...p} />,
};

// labelTone maps common observation-label keywords to a coloured pill so
// "Pressure" is always amber, "Flow" always sky, etc. Case-insensitive
// substring match; unknown labels get a neutral tone.
function labelTone(label: string): string {
  const l = label.toLowerCase();
  if (/pressure/.test(l)) return "bg-amber-900/60 text-amber-100";
  if (/flow|yield/.test(l)) return "bg-sky-900/60 text-sky-100";
  if (/weight|dose|mass/.test(l)) return "bg-emerald-900/60 text-emerald-100";
  if (/preinfus|infus/.test(l)) return "bg-indigo-900/60 text-indigo-100";
  if (/time|duration|timing|extract/.test(l)) return "bg-violet-900/60 text-violet-100";
  if (/temp|heat/.test(l)) return "bg-rose-900/60 text-rose-100";
  if (/grind|puck/.test(l)) return "bg-orange-900/60 text-orange-100";
  return "bg-neutral-800 text-neutral-200";
}

// RatingBadge renders the model's high-level grade of the shot as a large
// score + coloured label. We colour by score bucket so an "excellent" from
// one model and a "good" from another that both score 8 look consistent.
function RatingBadge({ rating }: { rating: { score: number; label?: string } }) {
  const score = Math.max(0, Math.min(10, Math.round(rating.score)));
  const tone =
    score >= 9
      ? { box: "border-emerald-700 bg-emerald-900/30", num: "text-emerald-300", chip: "bg-emerald-800/60 text-emerald-100" }
      : score >= 7
        ? { box: "border-lime-700 bg-lime-900/20", num: "text-lime-300", chip: "bg-lime-800/60 text-lime-100" }
        : score >= 5
          ? { box: "border-amber-700 bg-amber-900/20", num: "text-amber-300", chip: "bg-amber-800/60 text-amber-100" }
          : score >= 3
            ? { box: "border-orange-700 bg-orange-900/20", num: "text-orange-300", chip: "bg-orange-800/60 text-orange-100" }
            : { box: "border-rose-700 bg-rose-900/20", num: "text-rose-300", chip: "bg-rose-800/60 text-rose-100" };
  const label = rating.label?.trim() || bucketLabel(score);
  return (
    <div
      className={`flex min-w-[140px] flex-row items-center gap-4 rounded-lg border ${tone.box} px-5 py-4 md:flex-col md:items-start md:justify-center md:gap-2`}
    >
      <div className="flex items-baseline gap-1">
        <span className={`text-4xl font-semibold tabular-nums ${tone.num}`}>{score}</span>
        <span className="text-sm text-neutral-500">/ 10</span>
      </div>
      <div className="flex flex-col gap-0.5">
        <span
          className={`inline-flex w-fit items-center rounded-full px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide ${tone.chip}`}
        >
          {label}
        </span>
        <span className="text-[10px] uppercase tracking-wide text-neutral-500">Shot rating</span>
      </div>
    </div>
  );
}

function bucketLabel(score: number): string {
  if (score >= 9) return "excellent";
  if (score >= 7) return "good";
  if (score >= 5) return "fine";
  if (score >= 3) return "off";
  return "bad";
}
