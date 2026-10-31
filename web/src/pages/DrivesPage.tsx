import { attachRoutes, drivesAllQuery, drivesRoutesQuery } from "../lib/activityLists";
import { formatMoney } from "../lib/ledger";
import { appPath } from "../lib/demo";
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import ConnectRivianPrompt from "../components/ConnectRivianPrompt";
import { type Drive } from "../lib/api";
import { VehicleScopeNote } from "../components/VehicleScopeNote";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { DriveTable } from "../components/ActivityTables";
import { WindowPicker } from "../components/WindowPicker";
import { DrivesOverviewMap } from "../components/DriveMap";
import { filterByWindow, type WindowKey } from "../lib/analytics";
import { collapseRoundTrips, driveDurationSeconds } from "../lib/drives";
import { usePreferences } from "../lib/preferences";
import {
  formatDuration,
  num,
} from "../lib/format";

export default function DrivesPage() {
  const [win, setWin] = useState<WindowKey>("30d");
  // "table" is the default — numerical-first list — with "map" as a
  // geography overlay for the same windowed/collapsed row set.
  const [view, setView] = useState<"table" | "map">("table");
  const navigate = useNavigate();
  const {
    roundTripsEnabled,
    roundTripRadiusMeters,
    roundTripMaxGapMinutes,
  } = usePreferences();
  const q = useQuery(drivesAllQuery());
  // Routes are fetched only for the map view, which draws them.
  const routesQ = useQuery({ ...drivesRoutesQuery(), enabled: view === "map" });
  const rows = useMemo(() => {
    const filtered = filterByWindow(q.data ?? [], win);
    return roundTripsEnabled
      ? collapseRoundTrips(
          filtered,
          roundTripRadiusMeters,
          roundTripMaxGapMinutes,
        )
      : filtered;
  }, [q.data, win, roundTripsEnabled, roundTripRadiusMeters, roundTripMaxGapMinutes]);
  const totals = useMemo(() => summarize(rows), [rows]);

  return (
    <div className="space-y-4">
      <ConnectRivianPrompt context="Once connected, drives appear here automatically." />
      <PageHeader
        title="Drives"
        subtitle={
          q.data
            ? `${rows.length} of ${q.data.length} drive sessions`
            : undefined
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <ViewToggle value={view} onChange={setView} />
            <WindowPicker value={win} onChange={setWin} />
          </div>
        }
      />
      <VehicleScopeNote />
      <Card>
        {q.isLoading ? (
          <Spinner />
        ) : q.isError ? (
          <ErrorBox title="Failed to load drives" detail={String(q.error)} />
        ) : rows.length === 0 ? (
          <p className="text-sm text-neutral-400">
            No drives in this window.
          </p>
        ) : (
          <div className="space-y-3">
            <SummaryStrip totals={totals} />
            {view === "map" ? (
              <DrivesOverviewMap
                drives={attachRoutes(rows, routesQ.data)}
                onSelect={(id) => navigate(appPath(`/drives/${id}`))}
                height={480}
              />
            ) : (
              <DriveTable drives={rows} />
            )}
          </div>
        )}
      </Card>
    </div>
  );
}

// ViewToggle is the small Table / Map pill switch in the page header.
// Kept local to each list page — it's a one-of-two binary so a shared
// component would be over-engineered.
function ViewToggle({
  value,
  onChange,
}: {
  value: "table" | "map";
  onChange: (v: "table" | "map") => void;
}) {
  const opts: { v: "table" | "map"; label: string }[] = [
    { v: "table", label: "Table" },
    { v: "map", label: "Map" },
  ];
  return (
    <div className="inline-flex rounded-md border border-neutral-700 overflow-hidden text-xs">
      {opts.map((o) => (
        <button
          key={o.v}
          type="button"
          onClick={() => onChange(o.v)}
          className={`px-2.5 py-1 ${
            value === o.v
              ? "bg-emerald-900/40 text-emerald-200"
              : "bg-neutral-900 text-neutral-400 hover:bg-neutral-800"
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

// DriveTotals collapses the window-filtered rows into a topline.
// Avg speed weights by duration — a 10-minute city crawl shouldn't
// drag the number down as much as an hour of highway cruising. Max
// is just the max observed across the window.
type DriveTotals = {
  count: number;
  durationSec: number;
  distanceMi: number;
  avgSpeedMph: number;
  maxSpeedMph: number;
  energyKWh: number;
  cost: number;
  currency: string;
};

function summarize(rows: Drive[]): DriveTotals {
  const t: DriveTotals = {
    count: rows.length,
    durationSec: 0,
    distanceMi: 0,
    avgSpeedMph: 0,
    maxSpeedMph: 0,
    energyKWh: 0,
    cost: 0,
    currency: "",
  };
  let weightedSpeed = 0;
  for (const r of rows) {
    const dur = driveDurationSeconds(r);
    t.durationSec += dur;
    t.distanceMi += r.DistanceMi || 0;
    if (r.MaxSpeedMph > t.maxSpeedMph) t.maxSpeedMph = r.MaxSpeedMph;
    weightedSpeed += (r.AvgSpeedMph || 0) * dur;
    t.energyKWh += r.EnergyUsedKWh || 0;
    if (r.estimated_cost && r.estimated_cost > 0) {
      t.cost += r.estimated_cost;
      if (!t.currency && r.estimated_currency) t.currency = r.estimated_currency;
    }
  }
  if (t.durationSec > 0) t.avgSpeedMph = weightedSpeed / t.durationSec;
  return t;
}

function SummaryStrip({ totals }: { totals: DriveTotals }) {
  return (
    <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
      <Stat label="Drives" value={String(totals.count)} />
      <Stat label="Duration" value={formatDuration(totals.durationSec)} />
      <Stat label="Distance" value={num(totals.distanceMi, 1, "mi")} />
      <Stat
        label="Avg / Max"
        value={`${num(totals.avgSpeedMph, 0)} / ${num(totals.maxSpeedMph, 0)} mph`}
      />
      <Stat
        label="Energy"
        value={totals.energyKWh > 0 ? num(totals.energyKWh, 1, "kWh") : "—"}
      />
      <Stat
        label="Cost"
        value={
          totals.cost > 0
            ? `~${formatMoney(totals.cost, totals.currency || "USD")}`
            : "—"
        }
        hint={
          totals.cost > 0
            ? "each drive billed at the rate of its most recent prior charge"
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
