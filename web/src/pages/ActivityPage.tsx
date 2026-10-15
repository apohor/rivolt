import { displayTimeZone } from "../lib/format";
import { detailPath, inDemo } from "../lib/demo";
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { backend, type Charge } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { collapseRoundTrips } from "../lib/drives";
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
  const { roundTripsEnabled, roundTripRadiusMeters, roundTripMaxGapMinutes } = usePreferences();

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

  const days = useMemo(() => {
    const ds = drivesQ.data ?? [];
    const merged = roundTripsEnabled
      ? collapseRoundTrips(ds, roundTripRadiusMeters, roundTripMaxGapMinutes)
      : ds;
    return buildFeed(merged, chargesQ.data ?? [], {
      home: homeQ.data?.set ? homeQ.data : null,
      filter,
    });
  }, [drivesQ.data, chargesQ.data, homeQ.data, filter, roundTripsEnabled, roundTripRadiusMeters, roundTripMaxGapMinutes]);

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
          inDemo() ? undefined : (
          <span className="text-xs text-neutral-500">
            Tables:{" "}
            <Link to="/drives" className="text-neutral-300 hover:text-emerald-300">
              Drives
            </Link>
            {" · "}
            <Link to="/charges" className="text-neutral-300 hover:text-emerald-300">
              Charges
            </Link>
          </span>
          )
        }
      />
      <div role="tablist" aria-label="Filter" className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-sm">
        {FILTERS.map((f) => (
          <button
            key={f.key}
            type="button"
            role="tab"
            aria-selected={filter === f.key}
            onClick={() => {
              setParams(f.key === "all" ? {} : { f: f.key }, { replace: true });
              setShown(PAGE);
            }}
            className={`rounded-md px-3 py-1.5 ${filter === f.key ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
          >
            {f.label}
          </button>
        ))}
      </div>

      {drivesQ.isLoading || chargesQ.isLoading ? (
        <Spinner />
      ) : drivesQ.isError && chargesQ.isError ? (
        <ErrorBox title="Couldn't load activity" detail={String(drivesQ.error)} />
      ) : total === 0 ? (
        <Card>
          <p className="text-sm text-neutral-400">Nothing recorded yet. Drives and charges appear here as they happen.</p>
        </Card>
      ) : (
        <div className="space-y-5">
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
                    <li key={it.drive.ID}><DriveCard item={it} /></li>
                  ) : it.kind === "charge" ? (
                    <li key={it.charge.ID}><ChargeCard item={it} homeRate={homeRate} /></li>
                  ) : (
                    <li key={it.drives[0].drive.ID}><ErrandsRow item={it} /></li>
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
            ~{d.estimated_cost.toFixed(2)} {d.estimated_currency}
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
  const where = item.fast ? "Fast charge" : item.atHome ? "Home charge" : "Charge";
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
          {item.miles.toFixed(1)} mi{item.cost != null ? ` · ~${item.cost.toFixed(2)} ${item.currency}` : ""}
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
