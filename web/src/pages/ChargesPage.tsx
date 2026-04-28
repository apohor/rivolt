import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import {
  backend,
  type Charge,
  type ChargeCluster,
  type ChargeClusterLabel,
} from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { WindowPicker } from "../components/WindowPicker";
import { filterByWindow, type WindowKey } from "../lib/analytics";
import {
  durationSeconds,
  formatChargeState,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";

export default function ChargesPage() {
  const [win, setWin] = useState<WindowKey>("30d");
  // categoryFilter narrows the table + summary to a single charging
  // bucket. "" means "all categories" (the default). Stored in
  // component state because the filter is purely a view concern;
  // hitting reload starts you back on "all" which is what users want.
  const [categoryFilter, setCategoryFilter] =
    useState<ChargeClusterLabel | "">("");
  const q = useQuery({
    queryKey: ["charges", "all"],
    queryFn: () => backend.allCharges(),
  });
  // Clusters are derived from the full charge set server-side and
  // projected onto a map of chargeID → label for the table column.
  // A failed request is non-fatal: absence just means no badge.
  const cq = useQuery({
    queryKey: ["charges", "clusters"],
    queryFn: () => backend.chargeClusters(),
    staleTime: 60_000,
  });
  const labelByID = useMemo(() => labelMap(cq.data ?? []), [cq.data]);
  const windowed = useMemo(
    () => filterByWindow(q.data ?? [], win),
    [q.data, win],
  );
  const rows = useMemo(() => {
    if (!categoryFilter) return windowed;
    return windowed.filter((c) => labelByID.get(c.ID) === categoryFilter);
  }, [windowed, categoryFilter, labelByID]);
  const totals = useMemo(() => summarize(rows), [rows]);
  // Counts for each filter pill so the user knows what they'll get
  // before clicking. Computed off the windowed (pre-filter) set so
  // the pill labels stay stable while a filter is active.
  const counts = useMemo(() => {
    const c = { all: windowed.length, Home: 0, Public: 0, Fast: 0 };
    for (const r of windowed) {
      const l = labelByID.get(r.ID);
      if (l === "Home" || l === "Public" || l === "Fast") c[l]++;
    }
    return c;
  }, [windowed, labelByID]);

  return (
    <div className="space-y-4">
      <PageHeader
        title="Charges"
        subtitle={
          q.data
            ? `${rows.length} of ${q.data.length} charging sessions`
            : undefined
        }
        actions={<WindowPicker value={win} onChange={setWin} />}
      />
      <Card>
        {q.isLoading ? (
          <Spinner />
        ) : q.isError ? (
          <ErrorBox title="Failed to load charges" detail={String(q.error)} />
        ) : windowed.length === 0 ? (
          <p className="text-sm text-neutral-400">No charges in this window.</p>
        ) : (
          <div className="space-y-3">
            <CategoryFilter
              value={categoryFilter}
              onChange={setCategoryFilter}
              counts={counts}
            />
            <SummaryStrip totals={totals} />
            {rows.length === 0 ? (
              <p className="text-sm text-neutral-500">
                No {categoryFilter} charges in this window.
              </p>
            ) : (
              <ChargeTable charges={rows} labelByID={labelByID} />
            )}
          </div>
        )}
      </Card>
    </div>
  );
}

// CategoryFilter renders a horizontal pill row that toggles between
// All / Home / Public / Fast. Pills are click-to-select with the
// active one highlighted; counts trail each label so the user can
// see at a glance how the window splits across categories.
function CategoryFilter({
  value,
  onChange,
  counts,
}: {
  value: ChargeClusterLabel | "";
  onChange: (v: ChargeClusterLabel | "") => void;
  counts: { all: number; Home: number; Public: number; Fast: number };
}) {
  const pills: { label: string; value: ChargeClusterLabel | ""; count: number }[] = [
    { label: "All", value: "", count: counts.all },
    { label: "Home", value: "Home", count: counts.Home },
    { label: "Public", value: "Public", count: counts.Public },
    { label: "Fast", value: "Fast", count: counts.Fast },
  ];
  return (
    <div className="flex flex-wrap gap-2 text-xs">
      {pills.map((p) => {
        const active = value === p.value;
        return (
          <button
            key={p.label}
            type="button"
            onClick={() => onChange(p.value)}
            className={`rounded-full border px-3 py-1 tabular-nums transition ${
              active
                ? "border-emerald-700/60 bg-emerald-900/40 text-emerald-200"
                : "border-neutral-700 bg-neutral-900 text-neutral-400 hover:bg-neutral-800"
            }`}
          >
            {p.label}{" "}
            <span className="text-neutral-500">({p.count})</span>
          </button>
        );
      })}
    </div>
  );
}

function ChargeTable({
  charges,
  labelByID,
}: {
  charges: Charge[];
  labelByID: Map<string, ChargeClusterLabel>;
}) {
  const navigate = useNavigate();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs uppercase tracking-wide text-neutral-500">
            <th className="py-2 pr-4 font-medium">Start</th>
            <th className="py-2 pr-4 font-medium">Location</th>
            <th className="py-2 pr-4 font-medium">Duration</th>
            <th className="py-2 pr-4 font-medium">SoC</th>
            <th className="py-2 pr-4 font-medium">Energy</th>
            <th className="py-2 pr-4 font-medium">Max kW</th>
            <th className="py-2 pr-4 font-medium">Cost</th>
            <th className="py-2 pr-4 font-medium">Final state</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-neutral-800">
          {charges.map((c) => (
            <tr
              key={c.ID}
              className="cursor-pointer hover:bg-neutral-900/60"
              onClick={() => navigate(`/charges/${c.ID}`)}
            >
              <td className="py-2 pr-4 text-neutral-300 whitespace-nowrap">
                {formatDateTime(c.StartedAt)}
              </td>
              <td className="py-2 pr-4 whitespace-nowrap">
                <LocationBadge label={labelByID.get(c.ID) ?? ""} />
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {formatDuration(durationSeconds(c.StartedAt, c.EndedAt))}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {pct(c.StartSoCPct)} → {pct(c.EndSoCPct)}
              </td>
              <td className="py-2 pr-4 text-neutral-200 tabular-nums">
                {num(c.EnergyAddedKWh, 1, "kWh")}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {num(c.MaxPowerKW, 1)}
              </td>
              <td className="py-2 pr-4 text-neutral-400 tabular-nums">
                {c.Cost > 0
                  ? `${c.Cost.toFixed(2)} ${c.Currency ?? ""}`.trim()
                  : c.estimated_cost && c.estimated_cost > 0
                    ? `~${c.estimated_cost.toFixed(2)} ${c.estimated_currency ?? ""}`.trim()
                    : "—"}
              </td>
              <td className="py-2 pr-4 text-neutral-500">{formatChargeState(c.FinalState)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ChargeTotals collapses the filtered rows into a single topline.
// Cost prefers the persisted value snapshotted at charge-close time;
// for legacy/imported rows that lack one we fall back to the current
// home $/kWh rate as surfaced via estimated_cost on the same row.
// Currency is whatever the first row contributes — in practice all
// sessions for one operator share a currency.
type ChargeTotals = {
  count: number;
  durationSec: number;
  energyKWh: number;
  cost: number;
  estimated: boolean; // true if any portion of cost came from estimate
  currency: string;
};

function summarize(rows: Charge[]): ChargeTotals {
  const t: ChargeTotals = {
    count: rows.length,
    durationSec: 0,
    energyKWh: 0,
    cost: 0,
    estimated: false,
    currency: "",
  };
  for (const r of rows) {
    t.durationSec += durationSeconds(r.StartedAt, r.EndedAt);
    t.energyKWh += r.EnergyAddedKWh || 0;
    if (r.Cost > 0) {
      t.cost += r.Cost;
      if (!t.currency) t.currency = r.Currency;
    } else if (r.estimated_cost && r.estimated_cost > 0) {
      t.cost += r.estimated_cost;
      t.estimated = true;
      if (!t.currency && r.estimated_currency) t.currency = r.estimated_currency;
    }
  }
  return t;
}

function SummaryStrip({ totals }: { totals: ChargeTotals }) {
  return (
    <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <Stat label="Sessions" value={String(totals.count)} />
      <Stat label="Duration" value={formatDuration(totals.durationSec)} />
      <Stat label="Energy" value={num(totals.energyKWh, 1, "kWh")} />
      <Stat
        label="Cost"
        value={
          totals.cost > 0
            ? `${totals.estimated ? "~" : ""}${totals.cost.toFixed(2)}${totals.currency ? ` ${totals.currency}` : ""}`
            : "—"
        }
        hint={
          totals.estimated
            ? "includes estimated cost for sessions without a persisted price"
            : undefined
        }
      />
    </dl>
  );
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
    <div className="rounded-lg border border-neutral-800 bg-neutral-900/40 px-3 py-2">
      <dt className="text-[11px] uppercase tracking-wide text-neutral-500">
        {label}
      </dt>
      <dd className="mt-0.5 text-lg font-semibold tabular-nums text-neutral-100">
        {value}
      </dd>
      {hint ? (
        <div className="mt-0.5 text-[11px] text-neutral-500">{hint}</div>
      ) : null}
    </div>
  );
}

// labelMap projects the cluster response into a per-charge lookup the
// table uses to colour rows. Unknown-bucket clusters contribute an
// empty label so the UI renders a neutral dash.
function labelMap(clusters: ChargeCluster[]): Map<string, ChargeClusterLabel> {
  const m = new Map<string, ChargeClusterLabel>();
  for (const c of clusters) {
    const label: ChargeClusterLabel =
      c.label === "Home" || c.label === "Public" || c.label === "Fast"
        ? c.label
        : "";
    for (const id of c.member_ids) m.set(id, label);
  }
  return m;
}

// LocationBadge renders the Home/Public/Fast tag. Styling follows the
// convention of the rest of the app: Home leans emerald (positive /
// matches the "ready" pill on Settings), Fast is amber (attention —
// DCFC stops are the expensive ones), Public is plain neutral because
// it's the default catch-all for non-home slow charging.
function LocationBadge({ label }: { label: ChargeClusterLabel }) {
  if (!label) {
    return <span className="text-neutral-600">—</span>;
  }
  const tone =
    label === "Home"
      ? "border-emerald-600/40 text-emerald-300 bg-emerald-950/30"
      : label === "Fast"
        ? "border-amber-600/40 text-amber-300 bg-amber-950/30"
        : "border-neutral-700 text-neutral-400";
  return (
    <span
      className={`text-xs px-2 py-0.5 rounded-full border ${tone}`}
      title={
        label === "Fast"
          ? "Peak power ≥ 50 kW (DCFC)"
          : "Clustered by charge location"
      }
    >
      {label}
    </span>
  );
}
