import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { backend, type RestampResult, type RestampUndoResult } from "../lib/api";

// After the home location changes: "was the old one wrong, or did you move?". A dry run counts the past charges
// (and prices) that would change; nothing is re-classified until the user picks "wrong", and that can be undone.
// Shared by the Settings home panel and the charge page's "Use this charge's location as home".
export function useHomeRecheck() {
  const qc = useQueryClient();
  const [ask, setAsk] = useState<RestampResult | null>(null);
  const dry = useMutation({
    mutationFn: () => backend.restampPlace(true),
    onSuccess: (r) => setAsk(r.changed > 0 ? r : null),
  });
  const restamp = useMutation({
    mutationFn: () => backend.restampPlace(false),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ledger"] });
      qc.invalidateQueries({ queryKey: ["charges"] });
    },
  });
  const undo = useMutation({
    mutationFn: (id: string | null | undefined) => backend.restampPlaceUndo(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ledger"] });
      qc.invalidateQueries({ queryKey: ["charges"] });
    },
  });
  const start = () => {
    setAsk(null);
    restamp.reset();
    undo.reset();
    dry.mutate();
  };
  const reset = () => {
    setAsk(null);
    restamp.reset();
    undo.reset();
  };

  const prompt = (
    <>
      {ask && !restamp.isSuccess && (
        <div role="group" aria-label="Wrong pin or moved" className="rounded-md border border-amber-500/30 bg-amber-950/20 p-3">
          <p className="text-neutral-100">Was the old location wrong, or did you move?</p>
          <p className="mt-1 text-xs text-neutral-400">
            If it was wrong, {ask.changed} past {ask.changed === 1 ? "charge" : "charges"} will be re-checked against
            the new home: {ask.to_home} will move to Home and {ask.to_away} to away.{" "}
            {(ask.would_price_at_home_rate ?? 0) > 0
              ? `${ask.would_price_at_home_rate} unpriced home ${ask.would_price_at_home_rate === 1 ? "charge" : "charges"} will be priced at your home rate (+${(ask.total_cost_delta ?? 0).toFixed(2)} ${ask.currency ?? ""}). `
              : "Charges without a price stay unpriced (you can add prices). "}
            You can undo this right after. If you moved, history stays as recorded.
            {(ask.skipped_overridden ?? 0) > 0
              ? ` ${ask.skipped_overridden} ${ask.skipped_overridden === 1 ? "charge" : "charges"} you set by hand ${ask.skipped_overridden === 1 ? "stays" : "stay"} as you set ${ask.skipped_overridden === 1 ? "it" : "them"}.`
              : ""}
          </p>
          <div className="mt-2 flex flex-wrap gap-2">
            <button
              type="button"
              disabled={restamp.isPending}
              onClick={() => restamp.mutate()}
              className="rounded-md border border-emerald-700/50 bg-emerald-900/30 px-3 py-1.5 text-xs text-emerald-300 hover:bg-emerald-900/50 disabled:opacity-50"
            >
              {restamp.isPending ? "Updating…" : "It was wrong, re-check"}
            </button>
            <button
              type="button"
              disabled={restamp.isPending}
              onClick={() => setAsk(null)}
              className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-200 hover:bg-neutral-800"
            >
              I moved, keep history
            </button>
          </div>
          {restamp.isError && <p className="mt-2 text-xs text-rose-400">Couldn&apos;t update: {String(restamp.error)}</p>}
        </div>
      )}
      {restamp.isSuccess && !undo.isSuccess && (
        <div role="status" className="flex flex-wrap items-center gap-3 text-xs text-emerald-300">
          <span>
            Updated {restamp.data.changed} {restamp.data.changed === 1 ? "charge" : "charges"}
            {(restamp.data.would_price_at_home_rate ?? 0) > 0
              ? `, ${restamp.data.would_price_at_home_rate} priced at your home rate`
              : ""}
            .
          </span>
          <button
            type="button"
            disabled={undo.isPending}
            onClick={() => undo.mutate(restamp.data.restamp_id)}
            className="rounded-md border border-neutral-700 px-2 py-1 text-neutral-200 hover:bg-neutral-800 disabled:opacity-50"
          >
            {undo.isPending ? "Undoing…" : "Undo"}
          </button>
          {undo.isError && <span className="text-rose-400">Couldn&apos;t undo: {String(undo.error)}</span>}
        </div>
      )}
      {undo.isSuccess && (
        <p role="status" className="text-xs text-emerald-300">
          Restored {(undo.data as RestampUndoResult).restored_place} {(undo.data as RestampUndoResult).restored_place === 1 ? "charge" : "charges"}
          {(undo.data as RestampUndoResult).unpriced > 0 ? `; ${(undo.data as RestampUndoResult).unpriced} unpriced again` : ""}.
        </p>
      )}
    </>
  );
  return { start, reset, prompt };
}
