import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { backend, type Sample } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { LineChart } from "../components/charts";
import { ChargeMap } from "../components/DriveMap";
import {
  durationSeconds,
  formatChargeState,
  formatDateTime,
  formatDuration,
  isActiveCharge,
  num,
  pct,
} from "../lib/format";
import { formatTemperature, usePreferences } from "../lib/preferences";
import { smoothGaussianTime } from "../lib/smooth";

export default function ChargeDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const charges = useQuery({
    queryKey: ["charges", "all"],
    queryFn: () => backend.allCharges(),
  });

  const charge = useMemo(
    () => charges.data?.find((c) => c.ID === id),
    [charges.data, id],
  );

  const deleteCharge = useMutation({
    mutationFn: () => backend.deleteCharge(id!),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["charges"] });
      navigate("/charges", { replace: true });
    },
  });

  const updatePricing = useMutation({
    mutationFn: (body: {
      cost?: number;
      currency?: string;
      price_per_kwh?: number;
    }) => backend.updateChargePricing(id!, body),
    onSuccess: () => {
      // Charges are the input to the per-drive cost calculation, so
      // invalidate drives too — the next read will repaint with the
      // corrected rate flowing through.
      qc.invalidateQueries({ queryKey: ["charges"] });
      qc.invalidateQueries({ queryKey: ["drives"] });
    },
  });

  const samples = useQuery({
    queryKey: ["samples", "charge", id],
    enabled: !!charge,
    queryFn: () => {
      const since = new Date(
        new Date(charge!.StartedAt).getTime() - 60_000,
      );
      return backend.samples(since, 10_000);
    },
  });

  const chargeSamples = useMemo(() => {
    if (!charge || !samples.data) return [] as Sample[];
    const s = new Date(charge.StartedAt).getTime();
    const e = new Date(charge.EndedAt).getTime() + 60_000;
    return samples.data.filter((p) => {
      const t = new Date(p.At).getTime();
      return t >= s && t <= e;
    });
  }, [charge, samples.data]);

  const insights = useMemo(
    () => (charge ? computeSessionInsights(charge, chargeSamples) : null),
    [charge, chargeSamples],
  );
  const prefs = usePreferences();
  // Shared cursor across the Battery / Charger power / Temperature
  // charts on this page. Stored in milliseconds so it maps directly
  // onto each chart's x-axis (every series is keyed on Sample.At).
  const [cursorMs, setCursorMs] = useState<number | null>(null);

  if (charges.isLoading) {
    return (
      <div>
        <PageHeader title="Charge" />
        <Spinner />
      </div>
    );
  }
  if (!charge) {
    return (
      <div>
        <PageHeader title="Charge not found" />
        <Card>
          <p className="text-sm text-neutral-400">
            That charge ID doesn't exist in this dataset.{" "}
            <Link to="/charges" className="text-emerald-400 hover:underline">
              Back to charges →
            </Link>
          </p>
        </Card>
      </div>
    );
  }

  const socPtsRaw = chargeSamples.map((p) => ({
    x: new Date(p.At).getTime(),
    y: p.BatteryLevelPct || 0,
  }));
  const powerPtsRaw = chargeSamples
    .filter((p) => p.ChargerPowerKW > 0)
    .map((p) => ({
      x: new Date(p.At).getTime(),
      y: p.ChargerPowerKW,
    }));
  // Gaussian time-window smoothing: SoC ticks discretely in 1% steps,
  // charger power jitters several kW sample-to-sample. Charging
  // samples are 10–30s apart — use wider sigma for power.
  const socPts = smoothGaussianTime(socPtsRaw, 30_000);
  const powerPts = smoothGaussianTime(powerPtsRaw, 45_000);

  // Temperature series. Convert to the user's chosen unit at the
  // points level so the chart Y-axis, formatY label and the cursor
  // readout all stay consistent. Filter out the (0, 0) sentinel
  // emitted when the live merge path didn't carry a fresh reading
  // — a real 0 °C indoors is implausible and would distort the
  // Y-domain. Outside temp drifts slowly so a wide smoothing window
  // is appropriate.
  const tempUnit = prefs.temperatureUnit;
  const cToUnit = (c: number) => (tempUnit === "f" ? c * 1.8 + 32 : c);
  const tempUnitSuffix = tempUnit === "f" ? "°F" : "°C";
  const outsideTempPts = chargeSamples
    .filter((p) => Number.isFinite(p.OutsideTempC) && p.OutsideTempC !== 0)
    .map((p) => ({ x: new Date(p.At).getTime(), y: cToUnit(p.OutsideTempC) }));
  const insideTempPts = chargeSamples
    .filter((p) => Number.isFinite(p.InsideTempC) && p.InsideTempC !== 0)
    .map((p) => ({ x: new Date(p.At).getTime(), y: cToUnit(p.InsideTempC) }));
  const outsideTempSmoothed = smoothGaussianTime(outsideTempPts, 60_000);
  const insideTempSmoothed = smoothGaussianTime(insideTempPts, 60_000);
  const hasTempSeries =
    outsideTempSmoothed.length > 0 || insideTempSmoothed.length > 0;
  // Combined-chart overlay picks whichever signal is present.
  // Rivian live WS only exposes cabin (outside hardcoded to 0 in
  // internal/rivian/live.go); ElectraFi imports carry outside but
  // not cabin. Prefer outside; fall back to cabin.
  const ambientTempSeries =
    outsideTempSmoothed.length > 1
      ? { points: outsideTempSmoothed, label: "Outside temp" }
      : insideTempSmoothed.length > 1
        ? { points: insideTempSmoothed, label: "Cabin temp" }
        : null;

  // Resolve the sample closest to the synced cursor for the readout.
  const cursorSample = (() => {
    if (cursorMs == null || chargeSamples.length === 0) return null;
    let best = chargeSamples[0];
    let bestD = Math.abs(new Date(best.At).getTime() - cursorMs);
    for (let i = 1; i < chargeSamples.length; i++) {
      const d = Math.abs(new Date(chargeSamples[i].At).getTime() - cursorMs);
      if (d < bestD) {
        bestD = d;
        best = chargeSamples[i];
      }
    }
    return best;
  })();
  // For active live sessions the backend keeps `EndedAt` updated to
  // the last-seen sample, which makes the page look like the session
  // already ended. Compute duration against `now` and surface a
  // "charging now" subtitle instead so the UI matches reality.
  const active = isActiveCharge(charge);
  const duration = active
    ? Math.max(
        0,
        Math.floor((Date.now() - new Date(charge.StartedAt).getTime()) / 1000),
      )
    : durationSeconds(charge.StartedAt, charge.EndedAt);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Charge"
        subtitle={
          active
            ? `${formatDateTime(charge.StartedAt)} · charging now`
            : `${formatDateTime(charge.StartedAt)} → ${formatDateTime(charge.EndedAt)}`
        }
        actions={
          <Link
            to="/charges"
            className="text-xs text-neutral-400 hover:text-neutral-200"
          >
            ← all charges
          </Link>
        }
      />

      <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
        <Stat label="Duration" value={formatDuration(duration)} />
        <Stat
          label="SoC"
          value={`${pct(charge.StartSoCPct)} → ${pct(charge.EndSoCPct)}`}
        />
        <Stat label="Energy" value={num(charge.EnergyAddedKWh, 1, "kWh")} />
        <Stat label="Max kW" value={num(charge.MaxPowerKW, 1, "kW")} />
        <Stat
          label="Cost"
          value={chargeCostLabel(charge)}
          hint={chargeCostHint(charge)}
        />
      </div>

      <Card
        title={
          ambientTempSeries && powerPts.length > 0
            ? `Battery, charger power & ${ambientTempSeries.label.toLowerCase()}`
            : "Battery & charger power"
        }
      >
        {samples.isLoading ? (
          <Spinner />
        ) : socPts.length === 0 ? (
          <NoSamples />
        ) : (
          <>
            <LineChart
              series={[
                {
                  points: socPts,
                  color: "#10b981",
                  strokeWidth: 1.4,
                  area: true,
                  label: "Battery",
                },
                ...(powerPts.length > 0
                  ? [
                      {
                        points: powerPts,
                        color: "#f59e0b",
                        strokeWidth: 1.2,
                        label: "Power",
                        axis: "right" as const,
                      },
                    ]
                  : []),
                // Ambient-temperature overlay. Picks outside or
                // cabin depending on which signal exists for this
                // session (see ambientTempSeries). Mapped linearly
                // into the right (kW) axis so the dotted line stays
                // inside the chart frame; `formatCursor` inverts so
                // the readout still shows real °F/°C. Visual only —
                // the right axis labels remain kW. Suppressed when
                // there's no power signal: the right axis itself
                // disappears in that case and we don't want a lone
                // dotted line floating above an unlabeled axis.
                ...(() => {
                  if (powerPts.length === 0) return [];
                  if (!ambientTempSeries) return [];
                  const ys = ambientTempSeries.points.map((p) => p.y);
                  const tMin = Math.min(...ys);
                  const tMax = Math.max(...ys);
                  const span = Math.max(1, tMax - tMin);
                  const y2hi = powerYMax(charge.MaxPowerKW, powerPts);
                  const pad = y2hi * 0.05;
                  const lo = pad;
                  const hi = y2hi - pad;
                  const map = (t: number) =>
                    lo + ((t - tMin) / span) * (hi - lo);
                  const inv = (m: number) =>
                    tMin + ((m - lo) / Math.max(1e-9, hi - lo)) * span;
                  return [
                    {
                      points: ambientTempSeries.points.map((p) => ({
                        x: p.x,
                        y: map(p.y),
                      })),
                      color: "#fb923c",
                      strokeWidth: 1,
                      curve: "monotone" as const,
                      dash: "3 3",
                      axis: "right" as const,
                      label: ambientTempSeries.label,
                      formatCursor: (m: number) =>
                        `${inv(m).toFixed(0)}${tempUnitSuffix}`,
                    },
                  ];
                })(),
              ]}
              height={200}
              yDomain={[
                Math.max(0, charge.StartSoCPct - 5),
                Math.min(100, charge.EndSoCPct + 5),
              ]}
              y2Domain={[0, powerYMax(charge.MaxPowerKW, powerPts)]}
              formatY={(v) => `${v.toFixed(0)}%`}
              formatY2={(v) => `${v.toFixed(0)} kW`}
              formatX={xTimeFmt}
              cursorX={cursorMs}
              onCursorChange={setCursorMs}
            />
            <div className="mt-2 flex items-center gap-3 text-[10px] text-neutral-500">
              <span className="flex items-center gap-1">
                <span className="inline-block w-2 h-2 rounded-sm bg-emerald-500" />
                Battery (left)
              </span>
              {powerPts.length > 0 ? (
                <span className="flex items-center gap-1">
                  <span className="inline-block w-2 h-2 rounded-sm bg-amber-500" />
                  Power (right)
                </span>
              ) : (
                <span className="text-neutral-600">
                  No charger-power samples — see session card for context.
                </span>
              )}
              {ambientTempSeries && powerPts.length > 0 ? (
                <span className="flex items-center gap-1">
                  <span className="inline-block w-3 h-[2px] border-t border-dashed border-orange-400" />
                  {ambientTempSeries.label} ({tempUnitSuffix})
                </span>
              ) : null}
            </div>
          </>
        )}
      </Card>

      {/* When power is genuinely missing (home AC sessions; the
          ElectraFi pre-Mar-2026 export gap) we still want to surface
          the explanatory copy that used to live in the standalone
          "Charger power" card. Rendered only when the SoC chart is
          present and there's no power signal to overlay. */}
      {samples.isLoading || socPts.length === 0 || powerPts.length > 0 ? null : (
        <Card title="Charger power">
          <p className="text-sm text-neutral-500">
            {charge.Source === "live" ? (
              <>
                No charger-power samples for this session. Rivian's
                live feed reports <code>charger_power</code> only for
                DC fast-charging and the occasional Level 2 — home
                AC sessions come through with <code>0 kW</code>. Energy
                is reconstructed from the SoC delta at the session level.
              </>
            ) : (
              <>
                No charger-power samples recorded (the ElectraFi export
                stopped reporting <code>charger_power</code> for Rivians
                in Mar 2026). Energy and peak power are still
                reconstructed from SoC deltas at the session level.
              </>
            )}
          </p>
        </Card>
      )}

      {/* Temperature card. Renders when we have at least one
          non-sentinel reading in the session window. Joins the
          shared cursor with the Battery / Power charts above. */}
      {samples.isLoading ? null : hasTempSeries ? (
        <Card title="Temperature">
          <LineChart
            series={[
              ...(outsideTempSmoothed.length > 0
                ? [
                    {
                      points: outsideTempSmoothed,
                      color: "#60a5fa",
                      strokeWidth: 1.4,
                      label: "Outside",
                    },
                  ]
                : []),
              ...(insideTempSmoothed.length > 0
                ? [
                    {
                      points: insideTempSmoothed,
                      color: "#f97316",
                      strokeWidth: 1.2,
                      label: "Cabin",
                    },
                  ]
                : []),
            ]}
            height={140}
            formatY={(v) => `${v.toFixed(0)} ${tempUnitSuffix}`}
            formatX={xTimeFmt}
            cursorX={cursorMs}
            onCursorChange={setCursorMs}
          />
          <div className="mt-2 flex items-center gap-3 text-[10px] text-neutral-500">
            {outsideTempSmoothed.length > 0 ? (
              <span className="flex items-center gap-1">
                <span className="inline-block w-2 h-2 rounded-sm bg-sky-400" />
                Outside
              </span>
            ) : null}
            {insideTempSmoothed.length > 0 ? (
              <span className="flex items-center gap-1">
                <span className="inline-block w-2 h-2 rounded-sm bg-orange-500" />
                Cabin
              </span>
            ) : null}
          </div>
          {/* Compact cursor readout — only on this card; the page
              already has a busy session-insights summary, so we keep
              the inline value next to the temperature chart instead
              of replicating Drive's top-of-page banner. */}
          <div className="mt-2 h-4 text-[11px] font-mono text-neutral-400 flex items-center gap-3">
            {cursorSample ? (
              <>
                <span className="text-neutral-500">
                  {new Date(cursorSample.At).toLocaleTimeString(undefined, {
                    hour: "2-digit",
                    minute: "2-digit",
                    second: "2-digit",
                  })}
                </span>
                {cursorSample.OutsideTempC && cursorSample.OutsideTempC !== 0 ? (
                  <span className="text-sky-300">
                    Outside {formatTemperature(cursorSample.OutsideTempC, tempUnit, 0)}
                  </span>
                ) : null}
                {cursorSample.InsideTempC && cursorSample.InsideTempC !== 0 ? (
                  <span className="text-orange-300">
                    Cabin {formatTemperature(cursorSample.InsideTempC, tempUnit, 0)}
                  </span>
                ) : null}
              </>
            ) : null}
          </div>
        </Card>
      ) : null}

      <Card title="Session">
        <SessionInsights
          charge={charge}
          insights={insights}
          tempUnit={prefs.temperatureUnit}
        />
      </Card>

      {Number.isFinite(charge.Lat) && (charge.Lat !== 0 || charge.Lon !== 0) ? (
        <Card title="Location">
          <ChargeMap lat={charge.Lat} lon={charge.Lon} height={260} />
        </Card>
      ) : null}

      <PricingCard charge={charge} mutation={updatePricing} />

      <Card title="Danger zone">
        <p className="text-xs text-neutral-500 mb-2">
          Permanently removes this row from the database. Use this on
          obviously-broken sessions (e.g. SoC went down, or energy is
          way out of proportion to the SoC delta). Cannot be undone.
        </p>
        <button
          type="button"
          onClick={() => {
            if (
              window.confirm(
                `Delete charge ${charge.ID}? This cannot be undone.`,
              )
            ) {
              deleteCharge.mutate();
            }
          }}
          disabled={deleteCharge.isPending}
          className="px-3 py-1.5 text-xs rounded-md border border-rose-700/50 bg-rose-900/30 text-rose-300 hover:bg-rose-900/50 disabled:opacity-50"
        >
          {deleteCharge.isPending ? "Deleting…" : "Delete this charge"}
        </button>
        {deleteCharge.isError ? (
          <p className="mt-2 text-xs text-rose-400">
            {String(deleteCharge.error)}
          </p>
        ) : null}
      </Card>

      {samples.isError ? (
        <ErrorBox
          title="Sample data unavailable"
          detail={String(samples.error)}
        />
      ) : null}
    </div>
  );
}

function xTimeFmt(x: number): string {
  return new Date(x).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  });
}

// powerYMax picks a y-axis ceiling that snaps to common charging
// "tiers" so a 7 kW home session and a 250 kW RAN pull both look
// right. We pick the smallest tier that fits the session's peak,
// keeping a small headroom so the line doesn't kiss the top edge.
//
// Tiers reflect the canonical charger classes: L1 (1.4 kW), L2 home
// (7-11 kW), L2 commercial (19 kW), DC slow (50 kW), DC standard
// (150-250 kW), DC ultra (350 kW). Falls back to "max + 20%" if a
// session somehow blows past the highest tier.
function powerYMax(sessionMax: number, smoothed: { y: number }[]): number {
  let peak = sessionMax;
  for (const p of smoothed) if (p.y > peak) peak = p.y;
  if (peak <= 0) return 12;
  const tiers = [12, 25, 60, 120, 200, 300, 400];
  for (const t of tiers) if (peak <= t * 0.92) return t;
  return Math.ceil((peak * 1.2) / 50) * 50;
}

function Stat({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-3">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
      {hint ? (
        <div className="mt-1 text-[10px] text-neutral-500">{hint}</div>
      ) : null}
    </div>
  );
}

// chargeCostLabel returns the headline cost string for the Cost
// stat tile. Persisted Cost wins (Rivian-billed RAN session, or a
// manual override); otherwise we fall back to the estimated_cost
// the API attaches via the configured home rate. Em-dash if neither
// is available.
function chargeCostLabel(c: import("../lib/api").Charge): string {
  if (c.Cost > 0) {
    return `${c.Cost.toFixed(2)}${c.Currency ? ` ${c.Currency}` : ""}`;
  }
  if (c.estimated_cost && c.estimated_cost > 0) {
    return `~${c.estimated_cost.toFixed(2)}${c.estimated_currency ? ` ${c.estimated_currency}` : ""}`;
  }
  return "—";
}

// chargeCostHint annotates the headline cost with the per-kWh rate
// it implies, plus a flag noting whether this is a real billed
// number or a home-rate estimate. Falls back to undefined so the
// tile collapses cleanly when we have nothing useful to say.
function chargeCostHint(c: import("../lib/api").Charge): string | undefined {
  if (c.Cost > 0) {
    const ppk =
      c.PricePerKWh > 0
        ? c.PricePerKWh
        : c.EnergyAddedKWh > 0
          ? c.Cost / c.EnergyAddedKWh
          : 0;
    return ppk > 0
      ? `at ${ppk.toFixed(3)}${c.Currency ? ` ${c.Currency}` : ""}/kWh`
      : undefined;
  }
  if (c.estimated_cost && c.estimated_cost > 0 && c.EnergyAddedKWh > 0) {
    const ppk = c.estimated_cost / c.EnergyAddedKWh;
    return `estimated at ${ppk.toFixed(3)}${c.estimated_currency ? ` ${c.estimated_currency}` : ""}/kWh (home rate)`;
  }
  return undefined;
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-neutral-500">{label}</dt>
      <dd className="mt-1 tabular-nums">{value}</dd>
    </div>
  );
}

// SessionInsights is a richer reformulation of the old four-row
// Session card. We split the metrics into thematic sections so the
// user can scan them quickly: the top row is always-available
// "what happened", followed by power/timing and battery/energy
// blocks computed from the raw sample stream, and an environment
// block that only renders when temp data is present.
function SessionInsights({
  charge,
  insights,
  tempUnit,
}: {
  charge: import("../lib/api").Charge;
  insights: SessionInsightsData | null;
  tempUnit: import("../lib/preferences").TemperatureUnit;
}) {
  if (!insights) return null;
  const tier = insights.tier;
  return (
    <div className="space-y-4 text-sm text-neutral-300">
      <dl className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <Row
          label="Final state"
          value={formatChargeState(charge.FinalState)}
        />
        <Row label="Charging tier" value={tier.label} />
        <Row label="Source" value={charge.Source} />
        <Row
          label="Sessions like this"
          value={tier.hint}
        />
      </dl>

      <div>
        <h4 className="text-[10px] uppercase tracking-[0.12em] text-neutral-500 mb-2">
          Power &amp; timing
        </h4>
        <dl className="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <Row label="Peak power" value={num(charge.MaxPowerKW, 1, "kW")} />
          <Row label="Avg power" value={num(charge.AvgPowerKW, 1, "kW")} />
          <Row
            label="Time to peak"
            value={
              insights.timeToPeakSec > 0
                ? formatDuration(insights.timeToPeakSec)
                : "—"
            }
          />
          <Row
            label="Active charging"
            value={
              insights.activeChargingSec > 0
                ? `${formatDuration(insights.activeChargingSec)} (${pct(
                    insights.activeChargingPct,
                    0,
                  )})`
                : "—"
            }
          />
        </dl>
      </div>

      <div>
        <h4 className="text-[10px] uppercase tracking-[0.12em] text-neutral-500 mb-2">
          Battery &amp; energy
        </h4>
        <dl className="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <Row
            label="SoC gained"
            value={`${pct(insights.socGainedPct, 1)} (${pct(charge.StartSoCPct, 0)} → ${pct(charge.EndSoCPct, 0)})`}
          />
          <Row
            label="Charge rate"
            value={
              insights.socPerHour > 0
                ? `${insights.socPerHour.toFixed(1)} %/h`
                : "—"
            }
          />
          <Row
            label="Charge limit"
            value={
              insights.chargeLimitPct > 0
                ? pct(insights.chargeLimitPct, 0)
                : "—"
            }
          />
          <Row
            label="Miles added"
            value={num(charge.MilesAdded, 1, "mi")}
          />
          <Row
            label="Energy added"
            value={num(charge.EnergyAddedKWh, 2, "kWh")}
          />
          <Row
            label="Range efficiency"
            value={
              insights.miPerKWh > 0
                ? `${insights.miPerKWh.toFixed(2)} mi/kWh`
                : "—"
            }
          />
          <Row
            label="$/mile"
            value={insights.costPerMile || "—"}
          />
          <Row
            label="$/kWh"
            value={insights.costPerKWh || "—"}
          />
        </dl>
      </div>

      {insights.thermalKWh !== null ? (
        <div>
          <h4 className="text-[10px] uppercase tracking-[0.12em] text-neutral-500 mb-2">
            Thermal management
          </h4>
          <dl className="grid grid-cols-2 sm:grid-cols-4 gap-3">
            <Row
              label="BMS energy"
              value={`${insights.thermalKWh.toFixed(2)} kWh`}
            />
            <Row
              label="Share of total"
              value={
                insights.thermalSharePct !== null
                  ? pct(insights.thermalSharePct, 1)
                  : "—"
              }
            />
            <Row
              label="Conditioning intensity"
              value={thermalIntensityLabel(insights.thermalSharePct)}
            />
            <Row
              label="Source"
              value="Parallax live data"
            />
          </dl>
          <p className="mt-2 text-[11px] text-neutral-500">
            Energy the battery management system spent heating or
            cooling the pack during this session. Rivian's API doesn't
            expose pack temperature directly — high BMS energy means
            the pack was being aggressively conditioned (cold-soak
            DC fast-charge, hot-ambient L2, preconditioning before
            departure).
          </p>
        </div>
      ) : null}

      {insights.outsideTempC !== null || insights.insideTempC !== null ? (
        <div>
          <h4 className="text-[10px] uppercase tracking-[0.12em] text-neutral-500 mb-2">
            Environment
          </h4>
          <dl className="grid grid-cols-2 sm:grid-cols-4 gap-3">
            <Row
              label="Outside temp"
              value={formatTemperature(insights.outsideTempC, tempUnit, 0)}
            />
            <Row
              label="Inside temp"
              value={formatTemperature(insights.insideTempC, tempUnit, 0)}
            />
            <Row
              label="Started"
              value={formatDateTime(charge.StartedAt)}
            />
            <Row
              label="Ended"
              value={
                isActiveCharge(charge)
                  ? "In progress"
                  : formatDateTime(charge.EndedAt)
              }
            />
          </dl>
        </div>
      ) : null}
    </div>
  );
}

// SessionInsightsData is the shape of derived metrics consumed by
// the SessionInsights card. Kept narrow on purpose: only fields
// that drive the rendered rows go here, with units pre-baked.
type SessionInsightsData = {
  tier: { label: string; hint: string };
  timeToPeakSec: number;
  activeChargingSec: number;
  activeChargingPct: number; // share of total session spent at >0 kW
  socGainedPct: number;
  socPerHour: number; // %/hour over the whole session
  chargeLimitPct: number; // mode of ChargeLimitPct over the window
  miPerKWh: number; // MilesAdded / EnergyAddedKWh, when both > 0
  costPerMile: string; // pre-formatted, "" when unavailable
  costPerKWh: string;
  outsideTempC: number | null;
  insideTempC: number | null;
  // thermalKWh is null when not reported (legacy rows, non-Parallax
  // sources). thermalSharePct is computed only when both thermal and
  // total energy are > 0; otherwise null.
  thermalKWh: number | null;
  thermalSharePct: number | null;
};

// computeSessionInsights derives the metrics shown in the enriched
// Session card. Anything that requires raw samples is computed
// inline; pure-Charge-field metrics are computed from the row
// directly so the card still renders meaningfully when sample data
// is missing or stale.
function computeSessionInsights(
  charge: import("../lib/api").Charge,
  samples: Sample[],
): SessionInsightsData {
  const startMs = new Date(charge.StartedAt).getTime();
  const endMs = new Date(charge.EndedAt).getTime();
  const totalSec = Math.max(0, (endMs - startMs) / 1000);
  const totalHours = totalSec / 3600;

  // Active charging time: integrate the gaps between consecutive
  // samples whose ChargerPowerKW > 0. Caps each step at 5 minutes
  // so a long sample drop-out doesn't get counted as charging.
  const ACTIVE_STEP_CAP_SEC = 300;
  let activeSec = 0;
  for (let i = 1; i < samples.length; i++) {
    const prev = samples[i - 1];
    const cur = samples[i];
    if ((prev.ChargerPowerKW || 0) <= 0) continue;
    const dt =
      (new Date(cur.At).getTime() - new Date(prev.At).getTime()) / 1000;
    if (dt <= 0) continue;
    activeSec += Math.min(dt, ACTIVE_STEP_CAP_SEC);
  }
  const activePct = totalSec > 0 ? (activeSec / totalSec) * 100 : 0;

  // Time-to-peak: from session start to the first sample within
  // 90% of the max charger power. Helps surface ramp behaviour
  // (DC sessions ramp in seconds; L2 is essentially instant).
  let timeToPeakSec = 0;
  if (charge.MaxPowerKW > 0) {
    const target = charge.MaxPowerKW * 0.9;
    for (const s of samples) {
      if ((s.ChargerPowerKW || 0) >= target) {
        timeToPeakSec = Math.max(
          0,
          (new Date(s.At).getTime() - startMs) / 1000,
        );
        break;
      }
    }
  }

  // Charge limit: pick the mode of ChargeLimitPct across samples.
  // The user may bump the limit mid-session so mode beats first/last.
  const limitCounts = new Map<number, number>();
  for (const s of samples) {
    const l = Math.round(s.ChargeLimitPct || 0);
    if (l <= 0) continue;
    limitCounts.set(l, (limitCounts.get(l) || 0) + 1);
  }
  let chargeLimitPct = 0;
  let bestN = 0;
  for (const [l, n] of limitCounts) {
    if (n > bestN) {
      chargeLimitPct = l;
      bestN = n;
    }
  }

  // Average outside / inside temperature across the window. We use
  // a plain mean over present values — temps drift slowly enough
  // that a mean is more representative than mode.
  const outsideTempC = mean(samples.map((s) => s.OutsideTempC));
  const insideTempC = mean(samples.map((s) => s.InsideTempC));

  const socGainedPct = Math.max(0, charge.EndSoCPct - charge.StartSoCPct);
  const socPerHour = totalHours > 0 ? socGainedPct / totalHours : 0;

  const miPerKWh =
    charge.EnergyAddedKWh > 0 && charge.MilesAdded > 0
      ? charge.MilesAdded / charge.EnergyAddedKWh
      : 0;

  // Cost-per-X favours the persisted Cost; falls back to the home-
  // rate estimate so legacy / un-billed sessions still surface a
  // ballpark number with the right currency code.
  const cost = charge.Cost > 0 ? charge.Cost : charge.estimated_cost || 0;
  const costCurrency =
    charge.Cost > 0 ? charge.Currency : charge.estimated_currency || "";
  const isEstimate = charge.Cost <= 0;
  const costPerMile =
    cost > 0 && charge.MilesAdded > 0
      ? `${isEstimate ? "~" : ""}${(cost / charge.MilesAdded).toFixed(3)}${
          costCurrency ? ` ${costCurrency}` : ""
        }/mi`
      : "";
  const costPerKWh =
    cost > 0 && charge.EnergyAddedKWh > 0
      ? `${isEstimate ? "~" : ""}${(cost / charge.EnergyAddedKWh).toFixed(3)}${
          costCurrency ? ` ${costCurrency}` : ""
        }/kWh`
      : "";

  return {
    tier: classifyChargingTier(charge.MaxPowerKW),
    timeToPeakSec,
    activeChargingSec: activeSec,
    activeChargingPct: activePct,
    socGainedPct,
    socPerHour,
    chargeLimitPct,
    miPerKWh,
    costPerMile,
    costPerKWh,
    outsideTempC,
    insideTempC,
    thermalKWh:
      charge.ThermalKWh != null && charge.ThermalKWh >= 0
        ? charge.ThermalKWh
        : null,
    thermalSharePct:
      charge.ThermalKWh != null &&
      charge.ThermalKWh > 0 &&
      charge.EnergyAddedKWh > 0
        ? (charge.ThermalKWh / charge.EnergyAddedKWh) * 100
        : null,
  };
}

// classifyChargingTier maps a session's peak charger power to the
// canonical tier the operator would think in. Buckets borrow from
// SAE J1772 / CCS naming: L1 wall outlet, L2 home, L2 commercial,
// then DC slow / standard / fast / ultra.
function classifyChargingTier(maxKW: number): { label: string; hint: string } {
  if (maxKW <= 0) return { label: "—", hint: "no power data" };
  if (maxKW <= 2) return { label: "L1 (120 V)", hint: "wall outlet" };
  if (maxKW <= 12) return { label: "L2 home", hint: "7–11 kW range" };
  if (maxKW <= 25) return { label: "L2 commercial", hint: "destination charger" };
  if (maxKW <= 60) return { label: "DC slow", hint: "~50 kW DCFC" };
  if (maxKW <= 150) return { label: "DC standard", hint: "100–150 kW DCFC" };
  if (maxKW <= 250) return { label: "DC fast", hint: "150–250 kW DCFC" };
  return { label: "DC ultra", hint: "350 kW+ DCFC" };
}

// thermalIntensityLabel turns the thermal-share percentage into a
// human-friendly bucket. Buckets are calibrated against typical
// Rivian sessions: home L2 in mild weather is ~1–2% thermal; a
// cold-soaked DC fast-charge can push past 8%. The thresholds are
// heuristic, not from a Rivian spec — adjust as more data lands.
function thermalIntensityLabel(sharePct: number | null): string {
  if (sharePct === null) return "—";
  if (sharePct < 1) return "Minimal";
  if (sharePct < 3) return "Light";
  if (sharePct < 6) return "Moderate";
  if (sharePct < 10) return "Heavy";
  return "Aggressive";
}

// mean returns the arithmetic mean of finite, non-zero values, or
// null when no usable values are present. Zero is treated as
// "missing" because the ingester emits 0 for unset numeric columns.
function mean(values: number[]): number | null {
  let sum = 0;
  let n = 0;
  for (const v of values) {
    if (!Number.isFinite(v) || v === 0) continue;
    sum += v;
    n++;
  }
  return n > 0 ? sum / n : null;
}

function NoSamples() {
  return (
    <p className="text-sm text-neutral-500">
      No raw samples stored for this time window.
    </p>
  );
}

// PricingCard lets the user override the persisted price-per-kWh on
// a single charge — the escape hatch for DCFC sessions paid outside
// the Rivian app, where neither the live feed nor the home-rate
// fallback gets the right number. Total cost is derived from the
// rate × energy at save time so the rate stays the single source of
// truth (the same rate is what the per-drive cost calc consumes).
function PricingCard({
  charge,
  mutation,
}: {
  charge: import("../lib/api").Charge;
  // Loose typing so we don't pull react-query types just for this prop.
  mutation: {
    mutate: (body: {
      cost?: number;
      currency?: string;
      price_per_kwh?: number;
    }) => void;
    isPending: boolean;
    isError: boolean;
    error: unknown;
  };
}) {
  // Seed from PricePerKWh, but fall back to Cost/Energy so a row that
  // only has a total cost (legacy data, ElectraFi import) still
  // round-trips without surprising the user.
  const seed =
    charge.PricePerKWh > 0
      ? charge.PricePerKWh
      : charge.Cost > 0 && charge.EnergyAddedKWh > 0
        ? charge.Cost / charge.EnergyAddedKWh
        : 0;
  const [ppk, setPpk] = useState(seed > 0 ? seed.toFixed(3) : "");
  const [currency, setCurrency] = useState(charge.Currency || "USD");

  // Network price book — the user-managed list from
  // /api/settings/charging/networks. Failure is non-fatal: the
  // dropdown just doesn't appear and manual entry still works.
  const networks = useQuery({
    queryKey: ["charging-networks"],
    queryFn: () => backend.getChargingNetworks(),
    staleTime: 60_000,
  });

  const energy = charge.EnergyAddedKWh;
  const ppkNum = Number(ppk);
  const previewCost = ppkNum > 0 && energy > 0 ? ppkNum * energy : 0;

  return (
    <Card title="Pricing">
      <p className="text-xs text-neutral-500 mb-3">
        Override the per-kWh rate for this charge — handy when you
        paid for fast-charging outside the Rivian app and we don't
        have an upstream price. Total cost is derived from rate ×
        energy. Leave blank to clear the override and let the home
        rate take over again on read.
      </p>
      <form
        className="grid grid-cols-1 sm:grid-cols-2 gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          mutation.mutate({
            // Backend treats zero as "clear", and derives cost from
            // ppk × energy itself when ppk > 0 — we send 0 for cost
            // so the row stays consistent if the user later edits
            // EnergyAddedKWh in some other flow.
            cost: previewCost,
            currency: currency.trim().toUpperCase(),
            price_per_kwh: ppkNum > 0 ? ppkNum : 0,
          });
        }}
      >
        {networks.data && networks.data.length > 0 ? (
          <label className="block sm:col-span-2">
            <span className="block text-xs text-neutral-400 mb-1">
              Prefill from network
            </span>
            <select
              defaultValue=""
              onChange={(e) => {
                const n = networks.data!.find((x) => x.name === e.target.value);
                if (n) {
                  setPpk(n.price_per_kwh.toFixed(3));
                  setCurrency(n.currency || "USD");
                }
                // Reset back to placeholder so the same option can
                // be re-selected later (a no-op nudge for the user).
                e.target.value = "";
              }}
              className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-200 focus:border-emerald-500/60 focus:outline-none"
            >
              <option value="" disabled>
                Pick a network…
              </option>
              {networks.data.map((n) => (
                <option key={n.name} value={n.name}>
                  {n.name} — {n.price_per_kwh.toFixed(3)} {n.currency}/kWh
                </option>
              ))}
            </select>
          </label>
        ) : null}
        <label className="block">
          <span className="block text-xs text-neutral-400 mb-1">
            Price per kWh
          </span>
          <input
            type="number"
            step="0.001"
            min="0"
            inputMode="decimal"
            value={ppk}
            onChange={(e) => setPpk(e.target.value)}
            placeholder="e.g. 0.43"
            className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-200 tabular-nums focus:border-emerald-500/60 focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="block text-xs text-neutral-400 mb-1">
            Currency
          </span>
          <input
            type="text"
            value={currency}
            onChange={(e) => setCurrency(e.target.value.toUpperCase())}
            maxLength={4}
            className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm uppercase text-neutral-200 focus:border-emerald-500/60 focus:outline-none"
          />
        </label>
        <div className="sm:col-span-2 flex items-center justify-between gap-3 flex-wrap">
          <p className="text-xs text-neutral-500 tabular-nums">
            {previewCost > 0
              ? `≈ ${previewCost.toFixed(2)} ${currency} over ${energy.toFixed(1)} kWh`
              : `Energy: ${energy.toFixed(1)} kWh`}
          </p>
          <button
            type="submit"
            disabled={mutation.isPending}
            className="px-3 py-1.5 text-xs rounded-md border border-emerald-700/50 bg-emerald-900/30 text-emerald-300 hover:bg-emerald-900/50 disabled:opacity-50"
          >
            {mutation.isPending ? "Saving…" : "Save pricing"}
          </button>
        </div>
        {mutation.isError ? (
          <p className="sm:col-span-2 text-xs text-rose-400">
            {String(mutation.error)}
          </p>
        ) : null}
      </form>
    </Card>
  );
}
