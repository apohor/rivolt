import { useState } from "react";
import { inDemo } from "../lib/demo";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type PlanVsActual as Pva, type SavedTrip } from "../lib/api";
import { costHeader, drivenText, rows, stopLabel, stateOf, topCandidate, type Row } from "../lib/planVsActual";
import { Card } from "./ui";

// Plan vs Actual: for a saved trip, how the drive went against the plan
// (contract v1). Behind money_tab_enabled (the caller gates it). Never
// guesses: an incomplete cost reads "so far $X, N not priced yet" and
// shows no difference; a metric with an unknown side shows none.

export function PlanVsActualSection({ trips, highlightId }: { trips: SavedTrip[]; highlightId?: string | null }) {
  if (trips.length === 0) return null;
  return (
    <Card title="Plan vs actual" area="p-pva">
      <ul className="space-y-4">
        {trips.map((t) => (
          <li key={t.id} data-highlight={t.id === highlightId ? "true" : undefined} className={t.id === highlightId ? "rounded-lg ring-1 ring-emerald-700/60" : undefined}>
            <TripCompare trip={t} />
          </li>
        ))}
      </ul>
    </Card>
  );
}

function TripCompare({ trip }: { trip: SavedTrip }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["trips", "pva", trip.id],
    queryFn: () => backend.planVsActual(trip.id),
    retry: 0,
    staleTime: 60_000,
  });
  const set = (data: Pva) => qc.setQueryData(["trips", "pva", trip.id], data);
  const link = useMutation({ mutationFn: (tripID: string) => backend.planVsActualLink(trip.id, tripID), onSuccess: set });
  const unlink = useMutation({
    mutationFn: (tripID: string) => backend.planVsActualUnlink(trip.id, tripID),
    onSuccess: set,
  });
  const d = q.data;
  if (q.isLoading) return <div className="h-12 animate-pulse rounded bg-neutral-800/50" />;
  if (!d) return null; // endpoint absent: nothing to show
  const busy = link.isPending || unlink.isPending;
  const err = link.error ?? unlink.error;
  return (
    <div className="rounded-lg border border-neutral-800 p-3">
      <div className="text-sm font-medium text-neutral-100">{trip.name}</div>
      <Body d={d} busy={busy} readOnly={inDemo()} onConfirm={(id) => link.mutate(id)} onUnlink={(id) => unlink.mutate(id)} />
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
      <div className="mt-2 grid grid-cols-[1fr_auto_auto] gap-x-4 gap-y-2 text-sm">
        <div />
        <div className="text-right text-[11px] uppercase tracking-wide text-neutral-500">Planned</div>
        <div className="text-right text-[11px] uppercase tracking-wide text-neutral-500">Actual</div>
        {rs.map((r) => (
          <RowView key={r.key} r={r} currencyHint={c.currency} />
        ))}
      </div>
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

function RowView({ r, currencyHint }: { r: Row; currencyHint: string }) {
  const tone =
    r.tone === "good" ? "text-emerald-300" : r.tone === "watch" ? "text-amber-300" : "text-neutral-400";
  return (
    <>
      <div className="text-neutral-300">
        {r.label}
        {r.deltaText && <span className={`block text-xs tabular-nums ${tone}`}>{r.deltaText}</span>}
      </div>
      <div className="text-right tabular-nums text-neutral-400">{r.planned ?? "—"}</div>
      <div
        className={`text-right tabular-nums text-neutral-100 ${r.estimated ? "underline decoration-dotted underline-offset-4" : ""}`}
        title={r.estimated ? "Includes charging priced from a network rate, not billed" : undefined}
      >
        {r.incomplete ? (
          <span className="text-xs text-neutral-400">
            so far{" "}
            {new Intl.NumberFormat(undefined, { style: "currency", currency: currencyHint || "USD" }).format(r.incomplete.known)}
            , {r.incomplete.unpriced} not priced yet
          </span>
        ) : (
          (r.actual ?? "—")
        )}
      </div>
      {r.reasons.length > 0 && (
        <div className="col-span-3 -mt-1 text-xs text-neutral-500">
          {r.reasons.length > 1 ? "Mostly because " : ""}
          {r.reasons.map((x, i) => (
            <span key={i}>
              {i > 0 ? " Also: " : ""}
              {x}
            </span>
          ))}
        </div>
      )}
    </>
  );
}
