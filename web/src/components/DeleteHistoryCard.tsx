import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { cutoffChoices, deletedSummary } from "../lib/deleteHistory";
import { Card } from "./ui";

const btn = "rounded-md border border-rose-800 px-3 py-1.5 text-sm text-rose-200 hover:border-rose-600 disabled:cursor-not-allowed disabled:opacity-50";

// Settings → Data → "Delete older history": removes the drives, charges and telemetry from before a month you choose. Free on every plan
// (it is how an account on the free window clears what it no longer shows), never automatic, and gated by a ticked box.
export function DeleteHistoryCard({ now }: { now?: Date }) {
  const qc = useQueryClient();
  const choices = cutoffChoices(now);
  const [before, setBefore] = useState(choices[3]?.value ?? choices[0].value);
  const [sure, setSure] = useState(false);
  const del = useMutation({
    mutationFn: () => backend.deleteHistoryBefore(before),
    onSuccess: () => {
      setSure(false);
      qc.invalidateQueries();
    },
  });
  const label = choices.find((c) => c.value === before)?.label ?? before;
  return (
    <Card title="Delete older history" id="delete-history">
      <div data-delete-history className="space-y-3 text-sm">
        <p className="text-neutral-300">
          Deletes your drives, charges, recorded telemetry and their related receipts and notifications from before the first of a month (UTC). It works the same on every plan and cannot be undone.{" "}
          <Link to="/settings#export" className="underline underline-offset-2">Export everything first</Link> if you want a copy.
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <label htmlFor="delete-before" className="text-neutral-300">Delete everything before</label>
          <select id="delete-before" value={before} onChange={(e) => { setBefore(e.target.value); setSure(false); }} className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-neutral-100">
            {choices.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
          </select>
        </div>
        <label className="flex items-start gap-2 text-neutral-300">
          <input type="checkbox" checked={sure} onChange={(e) => setSure(e.target.checked)} className="mt-0.5" />
          <span>I understand this permanently deletes everything before {label}.</span>
        </label>
        <button type="button" className={btn} disabled={!sure || del.isPending} onClick={() => del.mutate()}>
          Delete history before {label}
        </button>
        {del.isSuccess && <p role="status" className="text-emerald-300">{deletedSummary(del.data)}</p>}
        {del.isError && <p role="alert" className="text-rose-400">Couldn&apos;t delete: {String((del.error as Error).message)}</p>}
      </div>
    </Card>
  );
}
