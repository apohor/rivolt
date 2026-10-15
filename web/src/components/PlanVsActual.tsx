import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type PlanVsActual as Pva, type SavedTrip } from "../lib/api";
import { drivenText, rows, STOP_LABEL, stateOf, topCandidate, type Row } from "../lib/planVsActual";
import { Card } from "./ui";

// Plan vs Actual: for a saved trip, how the drive went against the plan
// (contract v1). Behind money_tab_enabled (the caller gates it). Never
// guesses: an incomplete cost reads "so far $X, N not priced yet" and
// shows no difference; a metric with an unknown side shows none.

export function PlanVsActualSection({ trips }: { trips: SavedTrip[] }) {
  if (trips.length === 0) return null;
  return (
    <Card title="Plan vs actual">
      <ul className="space-y-4">
        {trips.map((t) => (
          <li key={t.id}>
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
  const unlink = useMutation({ mutationFn: () => backend.planVsActualUnlink(trip.id), onSuccess: set });
  const d = q.data;
  if (q.isLoading) return <div className="h-12 animate-pulse rounded bg-neutral-800/50" />;
  if (!d) return null; // endpoint absent: nothing to show
  const busy = link.isPending || unlink.isPending;
  const err = link.error ?? unlink.error;
  return (
    <div className="rounded-lg border border-neutral-800 p-3">
      <div className="text-sm font-medium text-neutral-100">{trip.name}</div>
      <Body d={d} busy={busy} onConfirm={(id) => link.mutate(id)} onUnlink={() => unlink.mutate()} />
      {err != null && <p className="mt-2 text-xs text-rose-400">Couldn&apos;t update: {String(err)}</p>}
    </div>
  );
}

function Body({
  d,
  busy,
  onConfirm,
  onUnlink,
}: {
  d: Pva;
  busy: boolean;
  onConfirm: (tripID: string) => void;
  onUnlink: () => void;
}) {
  const state = stateOf(d);
  if (state === "none") {
    return <p className="mt-1 text-sm text-neutral-400">We&apos;ll compare once you&apos;ve driven it.</p>;
  }
  const c = topCandidate(d);
  if (state === "suggested") {
    const when = c ? new Date(c.started_at).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "";
    return (
      <div className="mt-1">
        <p className="text-sm text-neutral-200">Was this your trip{when ? ` on ${when}` : ""}?</p>
        {d.comparison && <Compare d={d} preview />}
        <div className="mt-3 flex gap-2">
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
            disabled={busy}
            onClick={onUnlink}
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
      <button
        type="button"
        disabled={busy}
        onClick={onUnlink}
        className="mt-3 text-xs text-neutral-500 underline hover:text-neutral-300 disabled:opacity-50"
      >
        Not this trip? Unlink
      </button>
    </div>
  );
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
        {drivenText(c, null)}
        {preview ? " · preview" : ""}
        {c.plan_from === "snapshot" ? " · compared with the plan as saved" : ""}
        {cand && preview ? ` · ${cand.band} match` : ""}
      </p>
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
                  <span className={s.match === "skipped" || s.match === "extra" ? "text-amber-300" : "text-neutral-400"}>
                    {STOP_LABEL[s.match]}
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
      {r.reasons.length > 0 && <div className="col-span-3 -mt-1 text-xs text-neutral-500">{r.reasons[0]}</div>}
    </>
  );
}
