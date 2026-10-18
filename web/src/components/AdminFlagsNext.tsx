import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminFlagsState } from "../lib/api";
import { agoFrom } from "../lib/adminFormat";
import { Card, ErrorBox, Spinner } from "./ui";
import { TypedConfirmDialog } from "./TypedConfirmDialog";
import { AICallCapRow, AudienceFlagRow } from "./AdminFlagRows";

// Who changed a setting last, from the admin audit log: the newest row of the given action.
export function useLastChange(action: string) {
  const audit = useQuery({
    queryKey: ["admin", "audit", "last", action],
    queryFn: () => backend.adminAudit({ action, limit: 1 }),
    retry: 0,
    staleTime: 15_000,
  });
  const users = useQuery({ queryKey: ["admin", "users"], queryFn: () => backend.adminListUsers(), retry: 0, staleTime: 60_000 });
  const row = audit.data?.items?.[0];
  if (!row) return null;
  const u = users.data?.users.find((x) => x.id === row.actor_user_id);
  return { who: u ? u.display_name || u.email || u.username : "an admin", at: row.at };
}

export function ChangedBy({ action }: { action: string }) {
  const c = useLastChange(action);
  if (!c) return <span className="text-xs text-neutral-500">no changes recorded</span>;
  return (
    <span className="text-xs text-neutral-400" title={c.at}>
      Changed by {c.who} · {agoFrom(c.at)}
    </span>
  );
}

// Operations (new look): the Rivian kill switch on its own, with a typed confirm to pause it, and
// the other flags as rows with their value, who changed them and when, and a one-confirm toggle.
export function FlagsNext() {
  const qc = useQueryClient();
  const flags = useQuery({ queryKey: ["admin", "flags"], queryFn: backend.adminFlagsGet });
  const [pausing, setPausing] = useState(false);
  const [reason, setReason] = useState("");
  const [confirmPlanner, setConfirmPlanner] = useState(false);
  const refreshAudit = () => qc.invalidateQueries({ queryKey: ["admin", "audit"] });

  const kill = useMutation({
    mutationFn: ({ paused, reason }: { paused: boolean; reason: string }) => backend.adminFlagsKillPut(paused, reason),
    onSuccess: (data) => {
      qc.setQueryData(["admin", "flags"], (prev: AdminFlagsState | undefined) => ({
        ...prev,
        kill_switch: data.kill_switch,
        trip_planner: prev?.trip_planner ?? { enabled: false },
      }));
      refreshAudit();
    },
  });
  const planner = useMutation({
    mutationFn: (enabled: boolean) => backend.adminFlagsTripPlannerPut(enabled),
    onSuccess: (data) => {
      qc.setQueryData(["admin", "flags"], (prev: AdminFlagsState | undefined) => ({
        ...prev,
        kill_switch: prev?.kill_switch ?? { paused: false },
        trip_planner: data.trip_planner,
      }));
      refreshAudit();
    },
  });

  if (flags.isLoading) return <Spinner />;
  if (flags.isError) return <ErrorBox title="Failed to load flags" detail={(flags.error as Error).message} />;
  const f = flags.data!;
  const paused = f.kill_switch.paused;

  return (
    <div data-admin-flags className="space-y-4">
      <section aria-label="Rivian kill switch" className={`rounded-xl border p-4 ${paused ? "border-rose-600/60 bg-rose-950/30" : "border-neutral-800 bg-neutral-900/50"}`}>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-neutral-100">Rivian kill switch</h2>
            <p data-kill-state className={`mt-1 text-sm ${paused ? "font-medium text-rose-300" : "text-emerald-300"}`}>
              {paused ? `PAUSED${f.kill_switch.reason ? `: ${f.kill_switch.reason}` : ""}` : "Upstream calls are flowing"}
            </p>
            <p className="mt-1 text-xs text-neutral-400">Pauses every outbound Rivian call for every user.</p>
            <div className="mt-1"><ChangedBy action="flag.kill_switch" /></div>
          </div>
          {!pausing && (
            <button
              type="button"
              disabled={kill.isPending}
              onClick={() => (paused ? kill.mutate({ paused: false, reason: "" }) : setPausing(true))}
              className={`rounded-md border px-3 py-1.5 text-sm font-medium disabled:opacity-50 ${
                paused ? "border-emerald-600 bg-emerald-900/40 text-emerald-100 hover:bg-emerald-900/60" : "border-rose-700 text-rose-200 hover:bg-rose-900/40"
              }`}
            >
              {paused ? "Resume Rivian calls" : "Pause Rivian calls…"}
            </button>
          )}
        </div>
        {pausing && !paused && (
          <div className="mt-3">
            <TypedConfirmDialog
              title="Pause every Rivian call?"
              detail="Recording stops for every user until you resume. Use it when Rivian is blocking us or something is hammering them."
              phrase="PAUSE"
              confirmLabel="Pause Rivian calls"
              onCancel={() => { setPausing(false); setReason(""); }}
              onConfirm={() => { kill.mutate({ paused: true, reason }); setPausing(false); setReason(""); }}
            >
              <label className="mt-2 block text-xs text-neutral-300">
                Reason (recorded with the pause)
                <input value={reason} onChange={(e) => setReason(e.target.value)} className="input mt-1" />
              </label>
            </TypedConfirmDialog>
          </div>
        )}
        {kill.isError && <p role="alert" className="mt-2 text-xs text-rose-300">Save failed: {String((kill.error as Error).message)}</p>}
      </section>

      <Card title="Flags">
        <ul className="divide-y divide-neutral-800">
          <li data-flag="trip_planner" className="flex flex-wrap items-start justify-between gap-x-6 gap-y-2 py-3 first:pt-0">
            <div className="min-w-[220px] flex-1">
              <div className="flex items-center gap-2 text-neutral-100">
                Trip planner
                <span className={`rounded-full border px-2 py-0.5 text-xs ${f.trip_planner.enabled ? "border-emerald-700 text-emerald-300" : "border-neutral-600 text-neutral-300"}`}>
                  {f.trip_planner.enabled ? "On" : "Off"}
                </span>
              </div>
              <div className="mt-0.5 text-xs text-neutral-400">When off, the Plan link is hidden and the planner API answers 404.</div>
              <div className="mt-1"><ChangedBy action="flag.trip_planner" /></div>
            </div>
            <div>
              {confirmPlanner ? (
                <div className="inline-flex gap-1.5">
                  <button
                    type="button"
                    disabled={planner.isPending}
                    onClick={() => { planner.mutate(!f.trip_planner.enabled); setConfirmPlanner(false); }}
                    className="rounded-md bg-rose-700 px-2.5 py-1 text-xs font-semibold text-white hover:bg-rose-600"
                  >
                    Confirm {f.trip_planner.enabled ? "turn off" : "turn on"}
                  </button>
                  <button type="button" onClick={() => setConfirmPlanner(false)} className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-200">
                    Cancel
                  </button>
                </div>
              ) : (
                <button type="button" onClick={() => setConfirmPlanner(true)} className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-200 hover:border-neutral-500">
                  {f.trip_planner.enabled ? "Turn off" : "Turn on"}
                </button>
              )}
            </div>
          </li>
          {f.money_tab && (
            <AudienceFlagRow flag="money_tab" title="Money tab" feature="the Money tab" auditAction="flag.money_tab" value={f.money_tab} description="Who sees the Money tab (and the Truck and Activity tabs that come with it)." />
          )}
          {f.statement_email && (
            <AudienceFlagRow flag="statement_email" title="Monthly statement email" feature="the monthly statement email" auditAction="flag.statement_email" value={f.statement_email} description="Who can be sent the monthly statement." note="Each user must also opt in under Settings, Notifications." />
          )}
          {f.ai_call_cap && <AICallCapRow value={f.ai_call_cap} />}
        </ul>
        {planner.isError && <p role="alert" className="mt-2 text-xs text-rose-300">Save failed: {String((planner.error as Error).message)}</p>}
        <p className="mt-3 text-xs text-neutral-500">The signup cap is under Signups.</p>
      </Card>
    </div>
  );
}
