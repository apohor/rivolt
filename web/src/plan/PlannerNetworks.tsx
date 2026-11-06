import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type ChargingNetwork } from "../lib/api";

// The charging networks the person prefers, picked where they plan: the same "Prefer in planner" flag as Settings → Charging networks (the same list,
// saved the same way). Saving with a change plans the trip again. The planner favours a preferred network; it does not promise one.
export const NETWORKS_HONESTY = "Preferred networks are favoured by the planner, not guaranteed. Where one isn't on the way, you still get another.";

// The built-in networks (the ones the planner can be told about), preferred first, then by name.
export function listedNetworks(rows: ChargingNetwork[]): number[] {
  return rows
    .map((_, i) => i)
    .filter((i) => !!rows[i].slug)
    .sort((a, b) => Number(!!rows[b].preferred) - Number(!!rows[a].preferred) || rows[a].name.localeCompare(rows[b].name, undefined, { sensitivity: "base" }));
}

export function preferredChanged(rows: ChargingNetwork[], saved: ChargingNetwork[]): boolean {
  return rows.length !== saved.length || rows.some((r, i) => !!r.preferred !== !!saved[i]?.preferred);
}

export function PlannerNetworks({ onSaved }: { onSaved?: () => void }) {
  const qc = useQueryClient();
  const nets = useQuery({ queryKey: ["charging", "networks"], queryFn: () => backend.getChargingNetworks(), staleTime: 60_000 });
  const [rows, setRows] = useState<ChargingNetwork[]>([]);
  useEffect(() => {
    if (nets.data) setRows(nets.data);
  }, [nets.data]);
  const save = useMutation({
    mutationFn: () => backend.setChargingNetworks(rows),
    onSuccess: (saved) => {
      qc.setQueryData(["charging", "networks"], saved);
      onSaved?.();
    },
  });
  const changed = !!nets.data && preferredChanged(rows, nets.data);
  const listed = listedNetworks(rows);
  // Nothing until the list is read, and nothing when it cannot be or has no built-in network.
  if (!nets.data || listed.length === 0) return null;
  return (
    <details data-planner-networks className="rounded-lg border border-neutral-800 px-3 py-2 text-sm">
      <summary className="cursor-pointer text-neutral-200">Preferred charging networks</summary>
      <div className="mt-2 space-y-2">
        <p className="text-xs text-neutral-400">{NETWORKS_HONESTY}</p>
        <ul className="grid grid-cols-1 gap-1 sm:grid-cols-2">
          {listed.map((i) => (
            <li key={rows[i].slug}>
              <label className="inline-flex cursor-pointer items-center gap-2 text-neutral-200">
                <input
                  type="checkbox"
                  checked={!!rows[i].preferred}
                  onChange={(e) => setRows((r) => r.map((x, j) => (j === i ? { ...x, preferred: e.target.checked } : x)))}
                  className="h-4 w-4 accent-emerald-500"
                />
                <span>{rows[i].preferred ? "★" : "☆"} {rows[i].name}</span>
              </label>
            </li>
          ))}
        </ul>
        <div className="flex flex-wrap items-center gap-3">
          <button
            type="button"
            disabled={!changed || save.isPending}
            onClick={() => save.mutate()}
            className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50"
          >
            {save.isPending ? "Saving…" : "Save and plan again"}
          </button>
          {save.isError && <span role="status" className="text-xs text-amber-300">Couldn&apos;t save your networks. Nothing was changed.</span>}
          {save.isSuccess && !changed && <span role="status" className="text-xs text-neutral-400">Saved.</span>}
        </div>
      </div>
    </details>
  );
}
