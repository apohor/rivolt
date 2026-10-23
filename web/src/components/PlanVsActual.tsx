import { useState } from "react";
import { inDemo } from "../lib/demo";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, isPvaMulti, type PlanVsActual as Pva, type PlanVsActualMulti, type SavedTrip } from "../lib/api";
import { costHeader, drivenText, rows, stopLabel, stateOf, topCandidate, type Row } from "../lib/planVsActual";
import { Card } from "./ui";

// Plan vs Actual: for a saved trip, how the drive went against the plan
// (contract v1). Never
// guesses: an incomplete cost reads "so far $X, N not priced yet" and
// shows no difference; a metric with an unknown side shows none.

export function PlanVsActualSection({ trips, highlightId, ready = true }: { trips: SavedTrip[]; highlightId?: string | null; ready?: boolean }) {
  if (trips.length === 0) {
    // No saved trips: say what this tab is for, in the tone of the Saved tab, instead of a blank screen. Not while the list is still loading.
    if (!ready) return null;
    return (
      <Card title="Plan vs actual" area="p-pva">
        <p data-pva-empty className="text-xs text-neutral-500">
          Nothing to compare yet. Plan a route and save it, drive it, and Rivolt shows how the drive went against the plan here.
        </p>
      </Card>
    );
  }
  return <NextSection trips={trips} highlightId={highlightId} />;
}

// The latest matched trip as a compact bar comparison, unmatched
// and in-progress trips as one-line rows, older matched trips behind a toggle.
function NextSection({ trips, highlightId }: { trips: SavedTrip[]; highlightId?: string | null }) {
  const [more, setMore] = useState(false);
  const qs = useQueries({
    queries: trips.map((t) => ({
      queryKey: ["trips", "pva", t.id],
      queryFn: () => backend.planVsActual(t.id),
      retry: 0,
      staleTime: 60_000,
    })),
  });
  // Trips with a drive waiting for a Confirm / Not this trip always show (a suggestion nobody sees is never answered); the latest
  // confirmed comparison shows after them, older ones behind a toggle; trips not driven yet or still in progress are one-line rows.
  const needs: SavedTrip[] = [];
  const matched: SavedTrip[] = [];
  const lines: SavedTrip[] = [];
  trips.forEach((t, i) => {
    const d = qs[i].data;
    const parts = d ? (isPvaMulti(d) ? d.days : [d]) : [];
    const states = parts.map(stateOf);
    if (states.includes("suggested")) needs.push(t);
    else if (states.some((x) => x === "linked-full" || x === "linked-partial")) matched.push(t);
    else lines.push(t);
  });
  const shown = [...needs, ...(more ? matched : matched.slice(0, 1))];
  const hidden = matched.length - 1;
  // The trip open in the planner above gets the ring, as the classic card had.
  const ring = (id: string) => (id === highlightId ? "rounded-lg ring-1 ring-emerald-700/60" : undefined);
  return (
    <Card title="Plan vs actual" area="p-pva">
      <ul className="space-y-3" data-pva-next>
        {shown.map((t) => (
          <li key={t.id} data-highlight={t.id === highlightId ? "true" : undefined} className={ring(t.id)}>
            <TripCompare trip={t} compact />
          </li>
        ))}
        {lines.map((t) => (
          <li key={t.id} data-highlight={t.id === highlightId ? "true" : undefined} className={ring(t.id)}>
            <TripCompare trip={t} compact />
          </li>
        ))}
      </ul>
      {hidden > 0 && (
        <button
          type="button"
          aria-expanded={more}
          onClick={() => setMore((v) => !v)}
          className="mt-3 text-xs text-neutral-400 hover:text-neutral-200"
        >
          {more ? "Show fewer" : `Show ${hidden} more`}
        </button>
      )}
    </Card>
  );
}

function TripCompare({ trip, compact = false }: { trip: SavedTrip; compact?: boolean }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["trips", "pva", trip.id],
    queryFn: () => backend.planVsActual(trip.id),
    retry: 0,
    staleTime: 60_000,
  });
  // A single-day answer is the new data; a multi-day one is per day, so refetch the whole trip.
  const set = (data: Pva) => {
    const cur = qc.getQueryData<Pva | PlanVsActualMulti | null>(["trips", "pva", trip.id]);
    if (cur && isPvaMulti(cur)) qc.invalidateQueries({ queryKey: ["trips", "pva", trip.id] });
    else qc.setQueryData(["trips", "pva", trip.id], data);
  };
  const link = useMutation({
    mutationFn: (v: { tripID: string; day?: number }) => backend.planVsActualLink(trip.id, v.tripID, v.day),
    onSuccess: set,
  });
  const unlink = useMutation({
    mutationFn: (v: { tripID: string; day?: number }) => backend.planVsActualUnlink(trip.id, v.tripID, v.day),
    onSuccess: set,
  });
  const d = q.data;
  if (q.isLoading) return <div className="h-12 animate-pulse rounded bg-neutral-800/50" />;
  if (!d) return null; // endpoint absent: nothing to show
  const busy = link.isPending || unlink.isPending;
  const err = link.error ?? unlink.error;
  const parts = isPvaMulti(d) ? d.days : [d];
  // Not driven yet, or still in progress: the trip's name and the plain sentence, never a bare label. (A lost link keeps its card, which
  // has the Unlink button.)
  if (compact && parts.every((x) => ["none", "in-progress"].includes(stateOf(x)))) {
    const text = parts.length === 1 && stateOf(parts[0]) === "in-progress" ? "Trip in progress. We'll compare when you're home." : "We'll compare once you've driven it.";
    return (
      <div className="px-1 text-sm" data-pva-line>
        <div className="min-w-0 truncate text-neutral-200">{trip.name}</div>
        <p className="mt-0.5 text-xs text-neutral-400">{text}</p>
      </div>
    );
  }
  if (isPvaMulti(d)) {
    return (
      <div className="rounded-lg border border-neutral-800 p-3" data-pva-multiday>
        <div className="text-sm font-medium text-neutral-100">{trip.name}</div>
        <div className="mt-2 space-y-4">
          {d.days.map((x, i) => (
            <section
              key={x.day}
              aria-label={`Day ${x.day}: plan vs actual`}
              data-pva-day={x.day}
              className={i > 0 ? "border-t border-neutral-800 pt-4" : undefined}
            >
              <h4 className="text-xs font-medium uppercase tracking-wide text-neutral-400">Day {x.day}: plan vs actual</h4>
              {stateOf(x) === "none" ? (
                <p className="mt-1 text-sm text-neutral-400">Drive this day and the actuals appear here.</p>
              ) : (
                <Body
                  d={x}
                  busy={busy}
                  readOnly={inDemo()}
                  onConfirm={(id) => link.mutate({ tripID: id, day: x.day })}
                  onUnlink={(id) => unlink.mutate({ tripID: id, day: x.day })}
                />
              )}
            </section>
          ))}
        </div>
        <p className="mt-3 text-xs text-neutral-500">Actuals come from the drives and charges Rivolt recorded on that day.</p>
        {err != null && <p className="mt-2 text-xs text-rose-400">Couldn&apos;t update: {String(err)}</p>}
      </div>
    );
  }
  return (
    <div className="rounded-lg border border-neutral-800 p-3">
      <div className="text-sm font-medium text-neutral-100">{trip.name}</div>
      <Body d={d} busy={busy} readOnly={inDemo()} onConfirm={(id) => link.mutate({ tripID: id })} onUnlink={(id) => unlink.mutate({ tripID: id })} />
      {err != null && <p className="mt-2 text-xs text-rose-400">Couldn&apos;t update: {String(err)}</p>}
    </div>
  );
}

function Body({
  d,
  busy,
  readOnly = false,
  onConfirm,
  onUnlink,
}: {
  readOnly?: boolean;
  d: Pva;
  busy: boolean;
  onConfirm: (tripID: string) => void;
  onUnlink: (tripID: string) => void;
}) {
  const state = stateOf(d);
  if (state === "none") {
    return <p className="mt-1 text-sm text-neutral-400">We&apos;ll compare once you&apos;ve driven it.</p>;
  }
  if (state === "link-lost") {
    return (
      <div className="mt-1">
        <p className="text-sm text-neutral-400">We can&apos;t find the trip you linked any more.</p>
        <button
          type="button"
          disabled={busy || !d.link}
          onClick={() => d.link && onUnlink(d.link.trip_id)}
          className="mt-2 text-xs text-neutral-500 underline hover:text-neutral-300 disabled:opacity-50"
        >
          Unlink
        </button>
      </div>
    );
  }
  if (state === "in-progress") {
    return <p className="mt-1 text-sm text-neutral-400">Trip in progress. We&apos;ll compare when you&apos;re home.</p>;
  }
  const c = topCandidate(d);
  if (state === "suggested") {
    const when = c ? new Date(c.started_at).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "";
    return (
      <div className="mt-1">
        <p className="text-sm text-neutral-200">Was this your trip{when ? ` on ${when}` : ""}?</p>
        {d.comparison && <Compare d={d} preview />}
        <div className={`mt-3 flex gap-2 ${readOnly ? "hidden" : ""}`}>
          <button
            type="button"
            disabled={busy || !c}
            onClick={() => c && onConfirm(c.trip_id)}
            className="rounded-md border border-emerald-700/50 bg-emerald-900/30 px-3 py-1.5 text-xs text-emerald-300 hover:bg-emerald-900/50 disabled:opacity-50"
          >
            Confirm
          </button>
          <button
            type="button"
            disabled={busy || !c}
            onClick={() => c && onUnlink(c.trip_id)}
            className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-200 hover:bg-neutral-800 disabled:opacity-50"
          >
            Not this trip
          </button>
        </div>
      </div>
    );
  }
  return (
    <div className="mt-1">
      <Compare d={d} />
      {d.link?.link_stale && (
        <p className="mt-2 text-xs text-neutral-500">This match was re-found after your data changed; check it still looks right.</p>
      )}
      {!readOnly && (
        <button
          type="button"
          disabled={busy || !d.link}
          onClick={() => d.link && onUnlink(d.link.trip_id)}
          className="mt-3 text-xs text-neutral-500 underline hover:text-neutral-300 disabled:opacity-50"
        >
          Not this trip? Unlink
        </button>
      )}
    </div>
  );
}

// matchLabel: plain words for how sure we are the drive was this trip.
function matchLabel(band: string): string {
  if (band === "high") return "Likely match";
  if (band === "medium") return "Possible match";
  if (band === "low") return "Weak match";
  return "Match";
}

function Compare({ d, preview = false }: { d: Pva; preview?: boolean }) {
  const [showStops, setShowStops] = useState(false);
  const c = d.comparison!;
  const rs = rows(c);
  const cand = topCandidate(d);
  const incomplete = rs.some((r) => r.incomplete);
  return (
    <div className="mt-2">
      <p className="text-xs text-neutral-500">
        {drivenText(c, cand?.miles)}
        {c.plan_from === "snapshot" ? " · compared with the plan as saved" : ""}
        {cand && preview ? ` · ${matchLabel(cand.band)}` : ""}
      </p>
      {costHeader(c) && <p className="mt-2 text-sm font-medium text-neutral-100">{costHeader(c)}</p>}
      <BarRows rs={rs} currencyHint={c.currency} />
      {incomplete && (
        <p className="mt-3 text-xs text-amber-300">
          Some fast charging on this trip isn&apos;t priced yet, so the actual cost isn&apos;t shown. Add the prices
          in Money and this fills in.
        </p>
      )}
      {c.stops.length > 0 && (
        <div className="mt-3">
          <button
            type="button"
            aria-expanded={showStops}
            onClick={() => setShowStops((v) => !v)}
            className="text-xs text-neutral-400 hover:text-neutral-200"
          >
            {showStops ? "▾" : "▸"} Stops ({c.stops.length})
          </button>
          {showStops && (
            <ul className="mt-2 space-y-1.5 text-xs">
              {c.stops.map((s, i) => (
                <li key={i} className="flex flex-wrap items-baseline justify-between gap-2 rounded-md border border-neutral-800 px-2 py-1.5">
                  <span className="text-neutral-200">{s.planned?.name ?? s.actual?.network ?? "Stop"}</span>
                  <span className={s.match === "skipped" || (s.match === "extra" && s.actual?.kind !== "ac") ? "text-amber-300" : "text-neutral-400"}>
                    {stopLabel(s)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}


// Planned vs actual as paired bars per metric; deltas coloured by tone. A metric
// with an unknown side (or an incomplete cost) falls back to the text row.
function BarRows({ rs, currencyHint }: { rs: Row[]; currencyHint: string }) {
  return (
    <div className="mt-3 space-y-3" data-pva-bars>
      {rs.map((r) => {
        const tone = r.tone === "good" ? "text-emerald-300" : r.tone === "watch" ? "text-amber-300" : "text-neutral-400";
        const fill = r.tone === "good" ? "bg-emerald-500" : r.tone === "watch" ? "bg-amber-500" : "bg-sky-500";
        const bars = r.plannedN != null && r.actualN != null && Math.max(r.plannedN, r.actualN) > 0;
        const max = bars ? Math.max(r.plannedN!, r.actualN!) : 1;
        return (
          <div key={r.key}>
            <div className="flex items-baseline justify-between gap-3 text-sm">
              <span className="text-neutral-200">{r.label}</span>
              {r.deltaText && <span className={`text-xs tabular-nums ${tone}`}>{r.deltaText}</span>}
            </div>
            {bars ? (
              <div className="mt-1 space-y-1">
                <Bar label="Plan" value={r.planned ?? "—"} pct={(r.plannedN! / max) * 100} cls="bg-neutral-600" />
                <Bar label="Actual" value={r.actual ?? "—"} pct={(r.actualN! / max) * 100} cls={fill} dotted={r.estimated} />
              </div>
            ) : (
              <p className="mt-0.5 text-xs text-neutral-400">
                Plan <span className="tabular-nums text-neutral-300">{r.planned ?? "—"}</span>
                {" · "}
                {r.incomplete ? (
                  <span className="text-amber-300">
                    so far{" "}
                    {new Intl.NumberFormat(undefined, { style: "currency", currency: currencyHint || "USD" }).format(r.incomplete.known)}
                    , {r.incomplete.unpriced} not priced yet
                  </span>
                ) : (
                  <>Actual <span className="tabular-nums text-neutral-300">{r.actual ?? "—"}</span></>
                )}
              </p>
            )}
            {r.estimated && bars && (
              <p data-pva-estimated className="mt-1 text-xs text-neutral-500">Dotted bar: includes charging priced from a network rate, not billed.</p>
            )}
            {r.reasons.length > 0 && (
              <p className="mt-1 text-xs text-neutral-500" data-pva-reasons>
                {r.reasons.length > 1 ? "Mostly because " : ""}
                {r.reasons.map((x, i) => (
                  <span key={i}>
                    {i > 0 ? " Also: " : ""}
                    {x}
                  </span>
                ))}
              </p>
            )}
          </div>
        );
      })}
    </div>
  );
}

function Bar({ label, value, pct, cls, dotted = false }: { label: string; value: string; pct: number; cls: string; dotted?: boolean }) {
  return (
    <div className="flex items-center gap-2 text-xs">
      <span className="w-10 shrink-0 text-neutral-500">{label}</span>
      <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-neutral-800/70">
        <div className={`h-full rounded-full ${cls}`} style={{ width: `${Math.max(2, Math.min(100, pct))}%` }} />
      </div>
      <span className={`w-20 shrink-0 text-right tabular-nums text-neutral-200 ${dotted ? "underline decoration-dotted underline-offset-4" : ""}`}>{value}</span>
    </div>
  );
}
