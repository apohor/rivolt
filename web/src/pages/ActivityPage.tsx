import { formatMoney } from "../lib/ledger";
import { PeriodPicker } from "../components/PeriodPicker";
import { filterByRange, periodRange, usePeriod } from "../lib/period";
import { useUiNext } from "../lib/uiNext";
import { displayTimeZone } from "../lib/format";
import { detailPath, inDemo, appPath } from "../lib/demo";
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { backend, type Charge, type Drive } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { collapseRoundTrips } from "../lib/drives";
import { ACTIVITY_VIEWS, parseActivityView, withActivityView, type ActivityView } from "../lib/activityView";
import { ChargeTable, DriveTable, labelMap } from "../components/ActivityTables";
import { chargePlaceLabel } from "../lib/chargePlace";
import { ChargesOverviewMap, DrivesOverviewMap } from "../components/DriveMap";
import { usePreferences } from "../lib/preferences";
import { formatDuration, durationSeconds } from "../lib/format";
import {
  buildFeed,
  chargeCostLine,
  driveMissingEnergy,
  type ChargeItem,
  type DriveItem,
  type ErrandItem,
  type Filter,
} from "../lib/activityFeed";

// Activity v1 (ux-report S3): one day-grouped feed of drives and
// charges as cards. Auto-trips and plan-vs-actual wait on a backend
// trips contract. Gated by money_tab_enabled (see App.tsx).

const FILTERS: { key: Filter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "drives", label: "Drives" },
  { key: "charges", label: "Charges" },
];
const PAGE = 40;

export default function ActivityPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("f");
  const filter: Filter = raw === "drives" || raw === "charges" ? raw : "all";
  const [shown, setShown] = useState(PAGE);
  const navigate = useNavigate();
  const { roundTripsEnabled, roundTripRadiusMeters, roundTripMaxGapMinutes } = usePreferences();
  // ui_theme_next: the shared calendar scope filters the feed; off shows everything.
  const next = useUiNext();
  const [period, setPeriod] = usePeriod();
  const range = useMemo(() => (next ? periodRange(period) : { start: null, end: null }), [next, period]);
  // Table and Map views are a ui_theme_next layer; flag off ignores ?view.
  const view: ActivityView = next ? parseActivityView(params.get("view")) : "feed";
  // The All map shows one layer at a time (Drives or Charges), not two stacked maps.
  const [mapLayer, setMapLayer] = useState<"drives" | "charges">("drives");

  const drivesQ = useQuery({ queryKey: ["drives", "all"], queryFn: () => backend.allDrives() });
  const chargesQ = useQuery({ queryKey: ["charges", "all"], queryFn: () => backend.allCharges() });
  const homeQ = useQuery({
    queryKey: ["settings", "home-location"],
    queryFn: () => backend.homeLocationGet(),
    staleTime: 5 * 60_000,
    retry: 1,
  });

  const settingsQ = useQuery({
    queryKey: ["settings", "charging"],
    queryFn: () => backend.getChargingSettings(),
    staleTime: 5 * 60_000,
    retry: 1,
  });
  const homeRate = settingsQ.data?.home_price_per_kwh ?? null;
  // Charge location badges for the table; absence just means no badge.
  const clustersQ = useQuery({
    queryKey: ["charges", "clusters"],
    queryFn: () => backend.chargeClusters(),
    staleTime: 60_000,
    enabled: view === "table",
  });
  const labelByID = useMemo(() => labelMap(chargesQ.data ?? [], clustersQ.data ?? []), [chargesQ.data, clustersQ.data]);

  // The rows behind the Table and Map views: the same period and round-trip
  // collapse as the feed, before the All/Drives/Charges filter.
  const driveRows = useMemo<Drive[]>(() => {
    const ds = filterByRange(drivesQ.data ?? [], range);
    return roundTripsEnabled ? collapseRoundTrips(ds, roundTripRadiusMeters, roundTripMaxGapMinutes) : ds;
  }, [drivesQ.data, range, roundTripsEnabled, roundTripRadiusMeters, roundTripMaxGapMinutes]);
  const chargeRows = useMemo(() => filterByRange(chargesQ.data ?? [], range), [chargesQ.data, range]);
  const recordCount =
    (filter !== "charges" ? driveRows.length : 0) + (filter !== "drives" ? chargeRows.length : 0);
  const layer: "drives" | "charges" =
    filter === "drives" ? "drives" : filter === "charges" ? "charges" : driveRows.length === 0 ? "charges" : chargeRows.length === 0 ? "drives" : mapLayer;

  const days = useMemo(() => {
    const ds = filterByRange(drivesQ.data ?? [], range);
    const merged = roundTripsEnabled
      ? collapseRoundTrips(ds, roundTripRadiusMeters, roundTripMaxGapMinutes)
      : ds;
    return buildFeed(merged, filterByRange(chargesQ.data ?? [], range), {
      home: homeQ.data?.set ? homeQ.data : null,
      filter,
    });
  }, [drivesQ.data, chargesQ.data, homeQ.data, filter, range, roundTripsEnabled, roundTripRadiusMeters, roundTripMaxGapMinutes]);

  const total = days.reduce((n, d) => n + d.items.length, 0);
  let budget = shown;
  const visible = days
    .map((d) => {
      const items = d.items.slice(0, Math.max(0, budget));
      budget -= items.length;
      return { ...d, items };
    })
    .filter((d) => d.items.length > 0);

  return (
    <div className="space-y-4">
      <PageHeader
        title="Activity"
        subtitle="Drives and charges, newest first"
        // The table pages are signed-in only: no links to them in the demo.
        actions={
          <>
          {next && <PeriodPicker value={period} onChange={(p) => { setPeriod(p); setShown(PAGE); }} />}
          {next || inDemo() ? undefined : (
          <span className="text-xs text-neutral-500">
            Tables:{" "}
            <Link to={appPath("/drives")} className="text-neutral-300 hover:text-emerald-300">
              Drives
            </Link>
            {" · "}
            <Link to={appPath("/charges")} className="text-neutral-300 hover:text-emerald-300">
              Charges
            </Link>
          </span>
          )}
          </>
        }
      />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div role="tablist" aria-label="Filter" className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-sm">
          {FILTERS.map((f) => (
            <button
              key={f.key}
              type="button"
              role="tab"
              aria-selected={filter === f.key}
              onClick={() => {
                // Keep every other param: in the demo, `tab=activity` is what
                // keeps this view open.
                const next = new URLSearchParams(params);
                if (f.key === "all") next.delete("f");
                else next.set("f", f.key);
                setParams(next, { replace: true });
                setShown(PAGE);
              }}
              className={`rounded-md px-3 py-1.5 ${filter === f.key ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
            >
              {f.label}
            </button>
          ))}
        </div>
        {next && (
          <div role="group" aria-label="View" data-activity-views className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-sm">
            {ACTIVITY_VIEWS.map((v) => (
              <button
                key={v.key}
                type="button"
                aria-pressed={view === v.key}
                onClick={() => setParams(withActivityView(params, v.key), { replace: true })}
                className={`rounded-md px-3 py-1.5 ${view === v.key ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
              >
                {v.label}
              </button>
            ))}
          </div>
        )}
      </div>

      {drivesQ.isLoading || chargesQ.isLoading ? (
        <Spinner />
      ) : drivesQ.isError && chargesQ.isError ? (
        <ErrorBox title="Couldn't load activity" detail={String(drivesQ.error)} />
      ) : view !== "feed" ? (
        recordCount === 0 ? (
          <Card>
            <p className="text-sm text-neutral-400">
              {range.start || range.end
                ? "Nothing recorded in this period. Step to another period or pick All."
                : "Nothing recorded yet. Drives and charges appear here as they happen."}
            </p>
          </Card>
        ) : (
          <div className="space-y-5" data-activity-view={view}>
            {(drivesQ.isError || chargesQ.isError) && (
              <p role="status" className="text-xs text-amber-300">
                {drivesQ.isError ? "Drives" : "Charges"} couldn't be loaded, so this view is incomplete.
              </p>
            )}
            {view === "map" && filter === "all" && driveRows.length > 0 && chargeRows.length > 0 && (
              <div role="group" aria-label="Map layer" data-activity-layers className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-sm">
                {([["drives", "Drives", driveRows.length], ["charges", "Charges", chargeRows.length]] as const).map(([k, l, n]) => (
                  <button
                    key={k}
                    type="button"
                    aria-pressed={layer === k}
                    onClick={() => setMapLayer(k)}
                    className={`rounded-md px-3 py-1.5 ${layer === k ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
                  >
                    {l} <span className="text-neutral-500">{n}</span>
                  </button>
                ))}
              </div>
            )}
            {filter !== "charges" && driveRows.length > 0 && (view !== "map" || layer === "drives") && (
              <section aria-label="Drives">
                {filter === "all" && <h2 className="mb-2 text-xs uppercase tracking-wide text-neutral-500">Drives</h2>}
                <Card>
                  {view === "map" ? (
                    <DrivesOverviewMap drives={driveRows} onSelect={(id) => navigate(detailPath("drives", id))} height={380} />
                  ) : (
                    <DriveTable drives={driveRows} compact />
                  )}
                </Card>
              </section>
            )}
            {filter !== "drives" && chargeRows.length > 0 && (view !== "map" || layer === "charges") && (
              <section aria-label="Charges">
                {filter === "all" && <h2 className="mb-2 text-xs uppercase tracking-wide text-neutral-500">Charges</h2>}
                <Card>
                  {view === "map" ? (
                    <ChargesOverviewMap charges={chargeRows} onSelect={(id) => navigate(detailPath("charges", id))} height={380} />
                  ) : (
                    <ChargeTable charges={chargeRows} labelByID={labelByID} compact />
                  )}
                </Card>
              </section>
            )}
          </div>
        )
      ) : total === 0 ? (
        <Card>
          <p className="text-sm text-neutral-400">
            {range.start || range.end
              ? "Nothing recorded in this period. Step to another period or pick All."
              : "Nothing recorded yet. Drives and charges appear here as they happen."}
          </p>
        </Card>
      ) : (
        <div className="space-y-5" data-ui-feed>
          {(drivesQ.isError || chargesQ.isError) && (
            <p role="status" className="text-xs text-amber-300">
              {drivesQ.isError ? "Drives" : "Charges"} couldn't be loaded, so this feed is incomplete.
            </p>
          )}
          {visible.map((d) => (
            <section key={d.day} aria-label={d.day}>
              <h2 className="mb-2 text-xs uppercase tracking-wide text-neutral-500">{dayLabel(d.day)}</h2>
              <ul className="space-y-2">
                {d.items.map((it) =>
                  it.kind === "drive" ? (
                    <li key={it.drive.ID} data-kind="drive"><DriveCard item={it} /></li>
                  ) : it.kind === "charge" ? (
                    <li key={it.charge.ID} data-kind={it.fast ? "fast" : "charge"}><ChargeCard item={it} homeRate={homeRate} /></li>
                  ) : (
                    <li key={it.drives[0].drive.ID} data-kind="drive"><ErrandsRow item={it} /></li>
                  ),
                )}
              </ul>
            </section>
          ))}
          {shown < total && (
            <button
              type="button"
              onClick={() => setShown((n) => n + PAGE)}
              className="w-full rounded-lg border border-neutral-800 py-2 text-sm text-neutral-300 hover:bg-neutral-900"
            >
              Show more
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function dayLabel(key: string): string {
  const d = new Date(`${key}T12:00:00`);
  if (Number.isNaN(d.getTime())) return key;
  return d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
}

function timeRange(a: string, b: string): string {
  const f = (s: string) =>
    new Date(s).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", timeZone: displayTimeZone() });
  return `${f(a)}–${f(b)}`;
}

const CARD = "block rounded-xl border border-neutral-800 bg-neutral-900/50 p-3 hover:border-neutral-700";

function DriveCard({ item }: { item: DriveItem }) {
  const d = item.drive;
  const title = item.from || item.to ? `${item.from ?? "Away"} → ${item.to ?? "Away"}` : "Drive";
  const missing = driveMissingEnergy(d);
  const dur = d.DrivingSeconds && d.DrivingSeconds > 0 ? d.DrivingSeconds : durationSeconds(d.StartedAt, d.EndedAt);
  return (
    <Link to={detailPath("drives", d.ID)} className={CARD}>
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-sm text-neutral-100"><span aria-hidden>↗ </span>{title}</span>
        <span className="tabular-nums text-sm text-neutral-200">{d.DistanceMi.toFixed(1)} mi</span>
      </div>
      <div className="mt-1 flex items-baseline justify-between gap-2 text-xs text-neutral-500">
        <span>{timeRange(d.StartedAt, d.EndedAt)} · {formatDuration(dur)}</span>
        {missing ? (
          <span className="text-amber-300">no energy recorded</span>
        ) : d.estimated_cost != null ? (
          <span className="tabular-nums underline decoration-dotted underline-offset-4">
            ~{formatMoney(d.estimated_cost, d.estimated_currency || "USD")}
          </span>
        ) : (
          <span>{d.EnergyUsedKWh.toFixed(1)} kWh</span>
        )}
      </div>
    </Link>
  );
}

function ChargeCard({ item, homeRate }: { item: ChargeItem; homeRate: number | null }) {
  const c: Charge = item.charge;
  const cost = chargeCostLine(c, homeRate);
  // The shared label (lib/chargePlace); the feed's geofence guess only
  // where the server gave no place.
  const label = chargePlaceLabel(c);
  const where =
    label === "Fast" ? "Fast charge" : label === "Home" ? "Home charge" : label === "Away" ? "Away charge"
    : item.fast ? "Fast charge" : item.atHome ? "Home charge" : "Charge";
  const energy = c.EnergyAddedKWh > 0 ? `${c.EnergyAddedKWh.toFixed(1)} kWh` : "no energy recorded";
  return (
    <Link to={detailPath("charges", c.ID)} className={CARD}>
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-sm text-neutral-100">
          <span aria-hidden className={item.fast ? "text-amber-400" : "text-emerald-400"}>⚡ </span>
          {where} <span className="text-neutral-500">{Math.round(c.StartSoCPct)}→{Math.round(c.EndSoCPct)}%</span>
        </span>
        <span className={`tabular-nums text-sm ${c.EnergyAddedKWh > 0 ? "text-neutral-200" : "text-amber-300"}`}>{energy}</span>
      </div>
      <div className="mt-1 flex items-baseline justify-between gap-2 text-xs text-neutral-500">
        <span>{timeRange(c.StartedAt, c.EndedAt)}</span>
        <span
          className={`tabular-nums ${cost.tone === "missing" ? "text-amber-300" : cost.tone === "est" ? "underline decoration-dotted underline-offset-4" : ""}`}
        >
          {cost.text}
        </span>
      </div>
    </Link>
  );
}

function ErrandsRow({ item }: { item: ErrandItem }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900/30">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-baseline justify-between gap-2 p-3 text-left"
      >
        <span className="text-sm text-neutral-200">
          <span aria-hidden>{open ? "▾" : "▸"} </span>
          {item.drives.length} short drives
        </span>
        <span className="tabular-nums text-xs text-neutral-400">
          {item.miles.toFixed(1)} mi{item.cost != null ? ` · ~${formatMoney(item.cost, item.currency || "USD")}` : ""}
        </span>
      </button>
      {open && (
        <ul className="space-y-2 border-t border-neutral-800 p-2">
          {item.drives.map((d) => (
            <li key={d.drive.ID}><DriveCard item={d} /></li>
          ))}
        </ul>
      )}
    </div>
  );
}
