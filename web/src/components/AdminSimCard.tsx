import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminSimRun } from "../lib/api";
import { Card } from "./ui";

type Go = { kind: "drive" | "charge"; short?: boolean; then?: "charge" };
const BTN = "rounded-md border border-neutral-600 px-3 py-1.5 text-sm font-medium text-neutral-100 hover:bg-neutral-800 disabled:opacity-50";
const GOES: [string, Go][] = [
  ["Start a simulated drive now", { kind: "drive" }],
  ["Start a simulated charge now", { kind: "charge" }],
  ["Start a short drive now", { kind: "drive", short: true }],
  ["Start a short charge now", { kind: "charge", short: true }],
  ["Start a short drive, then a short charge", { kind: "drive", short: true, then: "charge" }],
];

const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

// Preview's simulated drive and charge: one run of about half an hour (or the short variant, about eight minutes, same speed and power) through the real recorder, from a few seconds from now. The
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
    mutationFn: async (go: Go) => {
      const first = await backend.adminSimStart(go.kind, { short: go.short });
      // The charge follows the drive straight on: queued, so the gate sees both without waiting between them.
      return go.then ? backend.adminSimStart(go.then, { short: go.short, queue: true }) : first;
    },
    onSuccess: (data) => qc.setQueryData(["admin", "sim"], data),
  });
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
          {GOES.map(([label, go]) => (
            <button key={label} type="button" disabled={busy} onClick={() => start.mutate(go)} className={BTN}>
              {label}
            </button>
          ))}
        </div>
        {run && (
          <p data-sim-active className="text-sm text-emerald-300">
            A simulated {run.short ? "short " : ""}{run.kind} runs {clock(run.start)} to {clock(run.end)}.
          </p>
        )}
        {start.isError && <p role="alert" className="text-sm text-rose-300">{(start.error as Error).message}</p>}
      </div>
    </Card>
  );
}
