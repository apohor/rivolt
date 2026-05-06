import { useEffect, useState } from "react";
import {
  backend,
  type DriveEfficiency,
  type EfficiencyFactor,
} from "../lib/api";
import { useAIEnabled } from "../lib/config";
import { usePreferences } from "../lib/preferences";
import { Card, ErrorBox, Spinner } from "./ui";
import { formatDateTime } from "../lib/format";

// EfficiencyExplainerCard renders an AI-driven breakdown of why a
// drive's efficiency landed where it did, with an actionable
// recommendation.
//
// Generation flow:
//   1. On mount we fetch the cached analysis from the server. If one
//      exists (drive was analyzed before, possibly in an earlier pod
//      lifecycle), we render it immediately — no LLM round-trip.
//   2. If no cache row exists, we show the empty-state form. Clicking
//      "Analyze efficiency" fires POST, which generates and persists
//      a fresh row, then renders the result.
//   3. "Regenerate" on an already-analyzed drive re-runs POST and
//      replaces the cached row. The user pays for that call.
//
// Persistence lives in the drive_efficiency table (migration 0025).
// Without it the card lost its content on every component remount
// AND every pod rollout — the previous design held the result in
// useState only, so a hard refresh wiped it.
export function EfficiencyExplainerCard({ driveId }: { driveId: string }) {
  const enabled = useAIEnabled();
  const prefs = usePreferences();
  const [data, setData] = useState<DriveEfficiency | null>(null);
  const [busy, setBusy] = useState(false);
  // Tracks the initial cache fetch so we render a small spinner
  // instead of the empty-state form while the GET is in flight. A
  // flash of the form on every drive open looks like the saved
  // analysis was lost.
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  // Per-trip transient context. Not persisted -- the user fills it
  // in (or leaves blank) before each Analyze click. Towing isn't a
  // form field: the backend infers it from the persisted driveMode
  // samples (Rivian's 'tow' / 'towing' drive mode), which the
  // efficiency analyzer already lists in the prompt.
  const [extraLoadLb, setExtraLoadLb] = useState<string>("");

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setData(null);
    setErr(null);
    backend
      .driveEfficiencyGet(driveId)
      .then((cached) => {
        if (cancelled) return;
        setData(cached);
      })
      .catch((e) => {
        if (cancelled) return;
        // GET failures are non-fatal — the user can still hit
        // Analyze. Surface the error so a misconfigured DB doesn't
        // silently look like "no analysis yet".
        setErr(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [driveId, enabled]);

  if (!enabled) return null;

  async function generate() {
    setBusy(true);
    setErr(null);
    try {
      const body: { extra_load_lb?: number; temperature_unit: "c" | "f" } = {
        // Echo the user's pref so the prompt's prose comments and
        // the recommendation use the right unit. The backend stores
        // Celsius internally and converts on prompt assembly.
        temperature_unit: prefs.temperatureUnit,
      };
      const n = Number(extraLoadLb);
      if (Number.isFinite(n) && n > 0) body.extra_load_lb = n;
      const fresh = await backend.driveEfficiencyGenerate(driveId, body);
      setData(fresh);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card
      title="Efficiency analysis"
      actions={
        data && !busy && !loading ? (
          <button
            type="button"
            onClick={generate}
            className="text-xs text-neutral-400 hover:text-neutral-200 underline-offset-2 hover:underline"
            title="Re-run the analysis (re-bills your AI provider)"
          >
            Regenerate
          </button>
        ) : null
      }
    >
      {loading ? (
        <div className="flex items-center gap-2 text-sm text-neutral-400">
          <Spinner />
          Loading analysis…
        </div>
      ) : busy ? (
        <div className="flex items-center gap-2 text-sm text-neutral-400">
          <Spinner />
          Analyzing drive…
        </div>
      ) : err ? (
        <ErrorBox title="Analysis failed" detail={err} />
      ) : !data ? (
        <div className="space-y-3 text-sm">
          <p className="text-xs text-neutral-500">
            Get an AI-driven breakdown of what drove this trip's
            efficiency — weather, terrain, driving style, climate
            control, tire pressure — with a single concrete
            recommendation for your next drive. Drive stats and
            elevation profile are sent; no GPS coordinates leave the
            box. The result is saved per-drive so you only pay the
            LLM once.
          </p>
          <div className="flex flex-wrap items-end gap-3">
            <div>
              <label
                htmlFor="eff-extra-load"
                className="block text-xs text-neutral-400 mb-1"
              >
                Extra load this trip
              </label>
              <div className="flex items-center gap-1">
                <input
                  id="eff-extra-load"
                  type="number"
                  inputMode="numeric"
                  min={0}
                  max={5000}
                  step={10}
                  placeholder="0"
                  value={extraLoadLb}
                  onChange={(e) => setExtraLoadLb(e.target.value)}
                  className="w-24 rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
                />
                <span className="text-xs text-neutral-500">lb</span>
              </div>
            </div>
            <button
              type="button"
              onClick={generate}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-500"
            >
              Analyze efficiency
            </button>
          </div>
        </div>
      ) : (
        <EfficiencyBody data={data} />
      )}
    </Card>
  );
}

function EfficiencyBody({ data }: { data: DriveEfficiency }) {
  const factors = data.factors ?? [];
  const hasStructured =
    factors.length > 0 ||
    !!data.recommendation ||
    !!data.forecast ||
    !!data.summary;
  return (
    <div className="space-y-4">
      {data.summary ? (
        <p className="text-sm leading-relaxed text-neutral-200">
          {data.summary}
        </p>
      ) : !hasStructured ? (
        <p className="text-sm leading-relaxed text-neutral-200 whitespace-pre-wrap">
          {data.analysis}
        </p>
      ) : null}

      {factors.length > 0 ? (
        <div className="space-y-1.5">
          <div className="text-[10px] uppercase tracking-wide text-neutral-500">
            Factors
          </div>
          <ul className="space-y-1">
            {factors.map((f, i) => (
              <FactorRow key={i} factor={f} />
            ))}
          </ul>
        </div>
      ) : null}

      {data.recommendation ? (
        <div className="rounded-md border border-emerald-800/50 bg-emerald-900/20 px-3 py-2">
          <div className="text-[10px] uppercase tracking-wide text-emerald-400/80">
            Recommendation
          </div>
          <p className="mt-1 text-sm leading-relaxed text-emerald-100/90">
            {data.recommendation}
          </p>
          {data.forecast ? (
            <p className="mt-1 text-xs text-emerald-300/70">
              {data.forecast}
            </p>
          ) : null}
        </div>
      ) : null}

      <div className="text-[11px] text-neutral-500">
        {data.model} · {formatDateTime(data.generated_at)}
      </div>
    </div>
  );
}

function FactorRow({ factor }: { factor: EfficiencyFactor }) {
  const pct = factor.impact_estimate_pct;
  const sign = pct > 0 ? "+" : "";
  const color =
    pct > 0
      ? "text-emerald-300"
      : pct < 0
      ? "text-amber-300"
      : "text-neutral-400";
  const conf = Math.max(0, Math.min(100, Math.round(factor.confidence_0_to_100)));
  return (
    <li className="flex items-center justify-between gap-3 text-sm">
      <span className="text-neutral-200">{factor.name}</span>
      <span className="flex items-center gap-2 tabular-nums">
        <span className={`font-medium ${color}`}>
          {sign}
          {pct.toFixed(0)}%
        </span>
        <span
          className="text-[10px] text-neutral-500"
          title={`Model confidence ${conf}/100`}
        >
          {conf}%
        </span>
      </span>
    </li>
  );
}
