import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { planDate, planState } from "../lib/plan";
import { PRO } from "../lib/proCopy";
import { Card } from "./ui";
import { ProSheet, trialLeft } from "./ProSheet";

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500";

// Settings → Account → Plan: where the account stands and the one next step. Shown only where the server bills; with billing off the
// account has everything and nothing here appears. Nothing is deleted when Pro ends: the card says so.
export function PlanCard({ now }: { now?: Date }) {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const [open, setOpen] = useState(false);
  const state = planState(me.data, now);
  if (state.kind === "off") return null;
  let line: string;
  let action: string;
  switch (state.kind) {
    case "free":
      line = "Free: the last 90 days of Now, Activity and Truck, and the current month's Money plus the two before it.";
      action = "See Rivolt Pro";
      break;
    case "trial":
      line = `Pro trial: ${trialLeft(state.ends, now)}${planDate(state.ends) ? ` (until ${planDate(state.ends)})` : ""}. ${PRO.webTrialNoCard}`;
      action = "Subscribe";
      break;
    case "pro":
      line =
        state.source === "founding"
          ? `Pro is on us${state.periodEnd ? ` until ${planDate(state.periodEnd)}` : ""}. No card, and nothing charges you.`
          : state.source === "grant"
            ? "Pro, granted to this account."
            : `Pro${state.periodEnd ? `, current period ends ${planDate(state.periodEnd)}` : ""}${state.source === "apple" ? " (billed by Apple)" : ""}.`;
      action = state.source === "stripe" ? PRO.manageStripe : state.source === "apple" ? "How to manage" : "What Pro includes";
      break;
    default:
      line = `Pro ended${planDate(state.endedOn) ? ` on ${planDate(state.endedOn)}` : ""}. You're on the free window; everything older is kept, stays in your export, and comes back if you subscribe.`;
      action = "See Rivolt Pro";
  }
  return (
    <Card title="Plan" id="plan">
      <div data-plan-card={state.kind} className="space-y-2">
        <p className="text-sm text-neutral-200">{line}</p>
        <button type="button" className={btn} onClick={() => setOpen(true)}>{action}</button>
      </div>
      {open && <ProSheet state={state} onClose={() => setOpen(false)} />}
    </Card>
  );
}
