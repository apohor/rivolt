import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { backend, type FromPosition, type TripPlanMultidayDay, type TripPlanMultidayRequest, type TripPlanMultidayResponse } from "../lib/api";
import { inDemo } from "../lib/demo";
import { planErrorCopy } from "./plannerHelpers";
import { mergeReplan, replanRequest } from "./replan";

const NO_POSITION_TITLE = "Your truck's position isn't available right now.";
const NO_POSITION_DETAIL = "Rivolt needs a recent reading from the truck to re-plan. Try again once it's awake.";

// ReplanFromHere: for a saved multi-day trip that is under way, plan the rest of it from where the
// truck is now. Only on an explicit tap: it asks Rivian's planner, so nothing here runs by itself.
export function ReplanFromHere({
  tripId,
  days,
  base,
  onPlanned,
}: {
  tripId: string;
  days: TripPlanMultidayDay[];
  // The request the trip was planned with, or one rebuilt from the form for an older save.
  base: () => TripPlanMultidayRequest | null;
  onPlanned: (merged: TripPlanMultidayResponse, info: { fromDay: number; position: FromPosition; mid: boolean }) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const active = useQuery({
    queryKey: ["trips", "active", tripId],
    queryFn: () => backend.savedTripActive(tripId),
    enabled: !inDemo(),
    retry: 0,
    staleTime: 30_000,
    // Cheap and read-only; refetches only while the page is visible.
    refetchInterval: 60_000,
  });
  const a = active.data;
  const prepared = a && a.kind === "multiday" && a.state === "active" ? (() => {
    const b = base();
    return b ? replanRequest(b, a) : null;
  })() : null;
  const run = useMutation({
    mutationFn: async () => {
      if (!prepared) throw new Error("nothing to re-plan");
      const resp = await backend.planTripMultiday(prepared.req);
      return { resp, fromDay: prepared.fromDay, mid: a?.phase !== "overnight" };
    },
    onSuccess: ({ resp, fromDay, mid }) => {
      setConfirming(false);
      const position = resp.from_position ?? a?.position;
      if (position) onPlanned(mergeReplan(days, fromDay, resp), { fromDay, position, mid });
    },
  });
  if (!a || a.kind !== "multiday" || a.state !== "active" || !a.current_day || !prepared) return null;
  const night = a.phase === "overnight" && !!a.next_day;
  const noPosition = !a.position;
  const err = run.error ? planErrorCopy(run.error) : null;
  return (
    <section data-replan className="rounded-lg border border-emerald-900/50 bg-emerald-950/20 p-3 text-sm" aria-label="Re-plan from here">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-neutral-200">
          {night
            ? `You're parked for the night; day ${a.next_day} of ${a.day_count} is next.`
            : `You're on day ${a.current_day} of ${a.day_count}.`}
        </p>
        {!confirming && (
          <button
            type="button"
            disabled={noPosition}
            onClick={() => setConfirming(true)}
            className="rounded-md border border-emerald-700/50 bg-emerald-900/30 px-3 py-1.5 text-xs text-emerald-300 hover:bg-emerald-900/50 disabled:opacity-50"
          >
            Re-plan from here
          </button>
        )}
      </div>
      {noPosition && (
        <p className="mt-2 text-xs text-neutral-400">
          <span className="font-medium text-neutral-300">{NO_POSITION_TITLE}</span> {NO_POSITION_DETAIL}
        </p>
      )}
      {confirming && (
        <div className="mt-3 space-y-2" role="group" aria-label="Confirm re-plan">
          <p className="text-neutral-300">
            Rivolt will plan the rest of {night ? "the trip" : "today"} from your truck&apos;s current location and charge ({Math.round(prepared.soc ?? 0)}%). The original plan is kept for comparison.
          </p>
          {prepared.passed.length > 0 && (
            <p className="text-xs text-neutral-400">Stops you&apos;ve already passed are left out: {prepared.passed.join(", ")}.</p>
          )}
          <div className="flex gap-2">
            <button
              type="button"
              disabled={run.isPending}
              onClick={() => run.mutate()}
              className="rounded-md bg-emerald-700 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-800 disabled:opacity-50"
            >
              {run.isPending ? "Re-planning…" : "Re-plan"}
            </button>
            <button
              type="button"
              disabled={run.isPending}
              onClick={() => {
                run.reset();
                setConfirming(false);
              }}
              className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-200 hover:bg-neutral-800 disabled:opacity-50"
            >
              Cancel
            </button>
          </div>
        </div>
      )}
      {err && (
        <p role="alert" className="mt-2 text-xs text-rose-300">
          <span className="font-medium">{err.title}</span> {err.detail}
        </p>
      )}
    </section>
  );
}
