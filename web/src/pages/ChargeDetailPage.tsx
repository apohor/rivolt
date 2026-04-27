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
  num,
  pct,
} from "../lib/format";
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
  const duration = durationSeconds(charge.StartedAt, charge.EndedAt);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Charge"
        subtitle={`${formatDateTime(charge.StartedAt)} → ${formatDateTime(charge.EndedAt)}`}
        actions={
          <Link
            to="/charges"
            className="text-xs text-neutral-400 hover:text-neutral-200"
          >
            ← all charges
          </Link>
        }
      />

      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <Stat label="Duration" value={formatDuration(duration)} />
        <Stat
          label="SoC"
          value={`${pct(charge.StartSoCPct)} → ${pct(charge.EndSoCPct)}`}
        />
        <Stat label="Energy" value={num(charge.EnergyAddedKWh, 1, "kWh")} />
        <Stat label="Max kW" value={num(charge.MaxPowerKW, 1, "kW")} />
      </div>

      <Card title="Battery state">
        {samples.isLoading ? (
          <Spinner />
        ) : socPts.length === 0 ? (
          <NoSamples />
        ) : (
          <LineChart
            series={[
              {
                points: socPts,
                color: "#10b981",
                strokeWidth: 1.4,
                area: true,
              },
            ]}
            height={180}
            yDomain={[
              Math.max(0, charge.StartSoCPct - 5),
              Math.min(100, charge.EndSoCPct + 5),
            ]}
            formatY={(v) => `${v.toFixed(0)}%`}
            formatX={xTimeFmt}
          />
        )}
      </Card>

      <Card title="Charger power">
        {samples.isLoading ? (
          <Spinner />
        ) : powerPts.length === 0 ? (
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
        ) : (
          <LineChart
            series={[
              {
                points: powerPts,
                color: "#f59e0b",
                strokeWidth: 1.2,
                area: true,
              },
            ]}
            height={160}
            yDomain={[0, powerYMax(charge.MaxPowerKW, powerPts)]}
            formatY={(v) => `${v.toFixed(0)} kW`}
            formatX={xTimeFmt}
          />
        )}
      </Card>

      <Card title="Session">
        <dl className="grid grid-cols-2 gap-3 text-sm text-neutral-300">
          <Row label="Final state" value={formatChargeState(charge.FinalState)} />
          <Row
            label="Avg power"
            value={num(charge.AvgPowerKW, 1, "kW")}
          />
          <Row
            label="Miles added"
            value={num(charge.MilesAdded, 1, "mi")}
          />
          <Row label="Source" value={charge.Source} />
        </dl>
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

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-3">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-neutral-500">{label}</dt>
      <dd className="mt-1 tabular-nums">{value}</dd>
    </div>
  );
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
