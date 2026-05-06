import { useState } from "react";
import {
  backend,
  type DriveEfficiency,
  type EfficiencyFactor,
} from "../lib/api";
import { useAIEnabled } from "../lib/config";
import { Card, ErrorBox, Spinner } from "./ui";
import { formatDateTime } from "../lib/format";

// EfficiencyExplainerCard renders an AI-driven breakdown of why a
// drive's efficiency landed where it did, with an actionable
// recommendation. Replaces the old TripRecapCard.
//
// Generation is on-demand and NOT cached — each click bills the
// operator's LLM key. Keeps state in local useState; nothing else in
// this codebase uses react-query mutations.
export function EfficiencyExplainerCard({ driveId }: { driveId: string }) {
  const enabled = useAIEnabled();
  const [data, setData] = useState<DriveEfficiency | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // Per-trip transient context. Not persisted -- the user fills these
  // in (or leaves blank) before each Analyze click. Persistent
  // per-vehicle settings (tire type, wheel size) come from
  // /api/vehicles/{id}/profile and are merged in by the backend.
  const [extraLoadLb, setExtraLoadLb] = useState<string>("");
  const [towing, setTowing] = useState(false);

  if (!enabled) return null;

  async function generate() {
    setBusy(true);
    setErr(null);
    try {
      const body: { extra_load_lb?: number; towing?: boolean } = {};
      const n = Number(extraLoadLb);
      if (Number.isFinite(n) && n > 0) body.extra_load_lb = n;
      if (towing) body.towing = true;
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
        data && !busy ? (
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
      {busy ? (
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
            box.
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
            <label className="flex items-center gap-2 pb-1.5 text-neutral-300">
              <input
                type="checkbox"
                checked={towing}
                onChange={(e) => setTowing(e.target.checked)}
                className="h-3.5 w-3.5 accent-emerald-500"
              />
              Towing this trip
            </label>
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
