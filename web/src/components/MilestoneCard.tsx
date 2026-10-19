import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { backend, type MilestoneItem } from "../lib/api";
import { Card } from "./ui";
import { ShareMilestoneButton } from "./ShareButtons";

// A milestone the truck just reached, on Now: the server's title and body as given, newest first, one at a
// time. Dismissing it shows the next one. Nothing renders when there is none or the server has no such call.
export default function MilestoneCard() {
  const q = useQuery({ queryKey: ["milestones"], queryFn: () => backend.milestones(), staleTime: 60_000, retry: 0 });
  const [gone, setGone] = useState<Set<string>>(new Set());
  const dismiss = useMutation({
    mutationFn: (m: MilestoneItem) => backend.milestoneDismiss(m.vehicle_id, m.key),
  });
  const id = (m: MilestoneItem) => `${m.vehicle_id}:${m.key}`;
  const current = (q.data ?? []).find((m) => !gone.has(id(m)));
  if (!current) return null;
  return (
    <Card>
      <div data-milestone className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-neutral-100">{current.title}</h2>
          <p className="mt-1 text-sm text-neutral-300">{current.body}</p>
          {dismiss.isError && <p role="alert" className="mt-1 text-xs text-rose-400">Couldn&apos;t hide that just now. Try again.</p>}
        </div>
        <div className="flex shrink-0 items-center gap-3">
        <ShareMilestoneButton milestone={current} />
        <button
          type="button"
          disabled={dismiss.isPending}
          onClick={() => {
            // Gone at once; if the server refuses, it comes back with the message above.
            setGone((g) => new Set(g).add(id(current)));
            dismiss.mutate(current, { onError: () => setGone((g) => { const n = new Set(g); n.delete(id(current)); return n; }) });
          }}
          className="text-xs text-neutral-400 hover:text-neutral-200"
        >
          Hide this
        </button>
        </div>
      </div>
    </Card>
  );
}
