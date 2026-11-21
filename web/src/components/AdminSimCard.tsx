import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminSimRun } from "../lib/api";
import { Card } from "./ui";

const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

// Preview's simulated drive and charge: one run of about half an hour through the real recorder, from a few seconds from now. The
// server mounts the routes only on the preview admin site, so anywhere else the first read is a 404 and the card is not drawn.
export function AdminSimCard() {
  const qc = useQueryClient();
  const sim = useQuery({
    queryKey: ["admin", "sim"],
    queryFn: backend.adminSimGet,
    retry: 0,
    refetchInterval: (q) => (q.state.data?.active ? 15_000 : false),
  });
  const start = useMutation({
    mutationFn: (kind: "drive" | "charge") => backend.adminSimStart(kind),
    onSuccess: (data) => qc.setQueryData(["admin", "sim"], data),
  });
  const [name, setName] = useState("");
  const history = useMutation({ mutationFn: () => backend.adminSampleHistory(name.trim()) });
  if (!sim.data) return null;
  const run: AdminSimRun | null = sim.data.active;
  const busy = start.isPending || run !== null;
  return (
    <Card title="Simulated drive and charge">
      <div data-admin-sim className="space-y-3">
        <p className="text-sm text-neutral-400">
          Plays one recorded run of about half an hour on the preview sample truck, through the real recorder, then returns to its normal day.
          Refused while the truck is driving or charging on its schedule, or when a scheduled one is close.
        </p>
        <div className="flex flex-wrap gap-2">
          <button type="button" disabled={busy} onClick={() => start.mutate("drive")} className="rounded-md border border-neutral-600 px-3 py-1.5 text-sm font-medium text-neutral-100 hover:bg-neutral-800 disabled:opacity-50">
            Start a simulated drive now
          </button>
          <button type="button" disabled={busy} onClick={() => start.mutate("charge")} className="rounded-md border border-neutral-600 px-3 py-1.5 text-sm font-medium text-neutral-100 hover:bg-neutral-800 disabled:opacity-50">
            Start a simulated charge now
          </button>
        </div>
        {run && (
          <p data-sim-active className="text-sm text-emerald-300">
            A simulated {run.kind} runs {clock(run.start)} to {clock(run.end)}.
          </p>
        )}
        <div className="space-y-2 border-t border-neutral-800 pt-3">
          <p className="text-sm text-neutral-400">
            Writes a year and a half of older drives, charges and weather behind the sample truck account&apos;s recorded history, so Pro&apos;s unlimited
            history has something to show. Safe to repeat.
          </p>
          <div className="flex flex-wrap gap-2">
            <input aria-label="Sample truck username" value={name} onChange={(e) => setName(e.target.value)} placeholder="username" className="rounded-md border border-neutral-600 bg-transparent px-2 py-1.5 text-sm text-neutral-100" />
            <button type="button" disabled={history.isPending || name.trim() === ""} onClick={() => history.mutate()} className="rounded-md border border-neutral-600 px-3 py-1.5 text-sm font-medium text-neutral-100 hover:bg-neutral-800 disabled:opacity-50">
              Write older history
            </button>
          </div>
          {history.data && <p data-sample-history-done className="text-sm text-emerald-300">Wrote {history.data.drives} drives and {history.data.charges} charges.</p>}
          {history.isError && <p role="alert" className="text-sm text-rose-300">{(history.error as Error).message}</p>}
        </div>
        {start.isError && <p role="alert" className="text-sm text-rose-300">{(start.error as Error).message}</p>}
      </div>
    </Card>
  );
}
