import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type PlanVsActual as Pva, type SavedTrip } from "../lib/api";
import { rows, stateOf, type Row } from "../lib/planVsActual";
import { Card } from "./ui";

// Plan vs Actual: for a saved trip, how the drive went against the
// plan. Behind money_tab_enabled (the caller gates it). Never guesses:
// an unpriced stop reads "price unavailable", a metric with an unknown
// side shows no difference.

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
  const link = useMutation({
    mutationFn: (tripID: string | null) => backend.planVsActualLink(trip.id, tripID),
    onSuccess: (data) => qc.setQueryData(["trips", "pva", trip.id], data),
  });
  const d = q.data;
  if (q.isLoading) return <div className="h-12 animate-pulse rounded bg-neutral-800/50" />;
  if (!d) return null; // endpoint absent: nothing to show
  return (
    <div className="rounded-lg border border-neutral-800 p-3">
      <div className="text-sm font-medium text-neutral-100">{trip.name}</div>
      <Body d={d} onConfirm={(id) => link.mutate(id)} onUnlink={() => link.mutate(null)} busy={link.isPending} />
      {link.isError && <p className="mt-2 text-xs text-rose-400">Couldn&apos;t update: {String(link.error)}</p>}
    </div>
  );
}

function Body({
  d,
  onConfirm,
  onUnlink,
  busy,
}: {
  d: Pva;
  onConfirm: (tripID: string) => void;
  onUnlink: () => void;
  busy: boolean;
}) {
  const state = stateOf(d);
  if (state === "none") {
    return <p className="mt-1 text-sm text-neutral-400">We&apos;ll compare once you&apos;ve driven it.</p>;
  }
  if (state === "suggested") {
    const c = d.candidate;
    const when = c ? new Date(c.started_at).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "";
    return (
      <div className="mt-1">
        <p className="text-sm text-neutral-200">
          Was this your trip{when ? ` on ${when}` : ""}?
          {c ? <span className="text-neutral-500"> {c.miles.toFixed(0)} mi</span> : null}
        </p>
        <div className="mt-2 flex gap-2">
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
            Unlink
          </button>
        </div>
      </div>
    );
  }
  const a = d.actual!;
  const rs = rows(d.planned, a);
  return (
    <div className="mt-2">
      <div className="grid grid-cols-[1fr_auto_auto] gap-x-4 gap-y-2 text-sm">
        <div className="text-[11px] uppercase tracking-wide text-neutral-500" />
        <div className="text-right text-[11px] uppercase tracking-wide text-neutral-500">Planned</div>
        <div className="text-right text-[11px] uppercase tracking-wide text-neutral-500">Actual</div>
        {rs.map((r) => (
          <RowView key={r.key} r={r} reason={d.reasons?.[r.key]} />
        ))}
      </div>
      {state === "matched-partial" && (
        <p className="mt-3 text-xs text-amber-300">
          Some charging on this trip isn&apos;t priced yet, so cost and its difference aren&apos;t shown. Add the
          prices in Money and this fills in.
        </p>
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

function RowView({ r, reason }: { r: Row; reason?: string }) {
  const tone =
    r.delta == null || r.delta === 0 ? "text-neutral-500" : r.key === "cost" || r.key === "duration" || r.key === "stops"
      ? r.delta > 0
        ? "text-amber-300"
        : "text-emerald-300"
      : "text-neutral-300";
  return (
    <>
      <div className="text-neutral-300">
        {r.label}
        {r.deltaText && <span className={`block text-xs tabular-nums ${tone}`}>{r.deltaText}</span>}
      </div>
      <div className="text-right tabular-nums text-neutral-400">{r.planned ?? "—"}</div>
      <div
        className={`text-right tabular-nums text-neutral-100 ${r.costNote === "estimated" ? "underline decoration-dotted underline-offset-4" : ""}`}
      >
        {r.actual ?? (r.costNote === "unknown" ? <span className="text-neutral-500">price unavailable</span> : "—")}
        {r.costNote === "partial" && <span className="ml-1 text-xs text-amber-300">partial</span>}
      </div>
      {reason && r.delta != null && r.delta !== 0 && (
        <div className="col-span-3 -mt-1 text-xs text-neutral-500">{reason}</div>
      )}
    </>
  );
}
