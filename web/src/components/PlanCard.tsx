import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { daysLeft, planState } from "../lib/plan";
import { planCardCopy } from "../lib/proCopy";
import { Card } from "./ui";
import { ProSheet } from "./ProSheet";

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500";

// "3 days left" for the trial line of the Settings card.
export function trialLeft(ends: string, now: Date = new Date()): string {
  const n = daysLeft(ends, now);
  return n == null ? "" : n === 0 ? "ends today" : `${n} ${n === 1 ? "day" : "days"} left`;
}

// Settings → Account → Plan: where the account stands and the one next step. Shown only where the server bills; with billing off (or
// the demo) the account has everything and nothing here appears. Nothing is deleted when Pro ends: the card says so.
export function PlanCard({ now }: { now?: Date }) {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const [open, setOpen] = useState(false);
  const state = planState(me.data);
  const copy = planCardCopy(state, state.kind === "trial" ? trialLeft(state.ends, now) : undefined);
  if (!copy) return null;
  return (
    <Card title="Plan" id="plan">
      <div data-plan-card={state.kind} className="space-y-2">
        <p className="text-sm font-medium text-neutral-100">{copy.title}</p>
        <p className="text-sm text-neutral-300">{copy.line}</p>
        {copy.action && <button type="button" className={btn} onClick={() => setOpen(true)}>{copy.action}</button>}
      </div>
      {open && <ProSheet state={state} onClose={() => setOpen(false)} />}
    </Card>
  );
}
