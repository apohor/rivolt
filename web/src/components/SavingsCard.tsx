import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { appPath } from "../lib/demo";
import { backend } from "../lib/api";
import { currentMonth, formatMoney, monthLabel, shiftMonth } from "../lib/ledger";
import { fetchSavings, leftOutLine, savedHeadline, savingsView } from "../lib/savings";
import { Card } from "./ui";

// SavingsCard: what moving home charging into the user's off-peak windows would have saved, from their own charges and schedule.
// Last full month by default, this month so far on request. Not shown when the server has no answer for the month.
export function SavingsCard({ area }: { area?: string }) {
  const [which, setWhich] = useState<"last" | "now">("last");
  const month = which === "last" ? shiftMonth(currentMonth(), -1) : currentMonth();
  const q = useQuery({ queryKey: ["savings", month], queryFn: ({ signal }) => fetchSavings(month, signal), staleTime: 5 * 60_000, retry: 0 });
  const settings = useQuery({ queryKey: ["charging-settings"], queryFn: () => backend.getChargingSettings(), staleTime: 60_000, retry: 0 });
  if (q.isError) return null;
  const currency = q.data?.currency || settings.data?.home_currency || "USD";
  const view = q.data ? savingsView(q.data) : null;

  return (
    <Card
      title="Off-peak savings"
      area={area}
      actions={
        <div className="inline-flex rounded-md border border-neutral-800 p-0.5 text-xs" role="group" aria-label="Month">
          {(["last", "now"] as const).map((k) => (
            <button
              key={k}
              type="button"
              aria-pressed={which === k}
              onClick={() => setWhich(k)}
              className={`rounded px-2 py-0.5 ${which === k ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
            >
              {k === "last" ? "Last month" : "This month so far"}
            </button>
          ))}
        </div>
      }
    >
      <div data-savings className="space-y-2 text-sm">
        <p className="text-xs text-neutral-500">{monthLabel(month)}</p>
        {!view ? (
          <p className="text-neutral-500">…</p>
        ) : view.kind === "needs" ? (
          <p className="text-neutral-300">
            {view.need === "schedule" && (
              <>
                Add your off-peak hours and rate to see what moving home charging would have saved.{" "}
                <Link to={appPath("/settings?tab=charging#offpeak")} className="text-emerald-300 underline underline-offset-4 hover:text-emerald-200">
                  Set off-peak hours
                </Link>
              </>
            )}
            {view.need === "home_rate" && (
              <>
                Set your home rate first, so hours outside your off-peak windows are priced.{" "}
                <Link to={appPath("/settings?tab=charging#cost")} className="text-emerald-300 underline underline-offset-4 hover:text-emerald-200">
                  Set home rate
                </Link>
              </>
            )}
            {view.need === "sessions" && <>A few more home charges are needed before this can be estimated for {monthLabel(month)}.</>}
          </p>
        ) : view.kind === "nothing" ? (
          <>
            <p className="text-neutral-300">Nothing to move in {monthLabel(month)}: your home charging already ran at your lowest rate, or no session fit a cheaper window.</p>
            {view.leftOut > 0 && <p className="text-xs text-neutral-500">{leftOutLine(view.leftOut)}{view.reasons.length > 0 && <> Why: {view.reasons.join("; ")}.</>}</p>}
          </>
        ) : (
          <>
            <p className="text-neutral-200">{savedHeadline(view.moved, formatMoney(view.saved, currency))}</p>
            {view.asCharged != null && view.ifMoved != null && (
              <p className="text-xs text-neutral-400">
                Home charging as charged {formatMoney(view.asCharged, currency)} · if moved {formatMoney(view.ifMoved, currency)} (estimate)
              </p>
            )}
            {view.considered != null && <p className="text-xs text-neutral-500">Based on {view.considered} home {view.considered === 1 ? "charge" : "charges"}.</p>}
            {view.basis && <p className="text-xs text-neutral-500">{view.basis}</p>}
            {view.leftOut > 0 && (
              <p className="text-xs text-neutral-500">
                {leftOutLine(view.leftOut)}
                {view.reasons.length > 0 && <> Why: {view.reasons.join("; ")}.</>}
              </p>
            )}
          </>
        )}
        <p className="text-[11px] text-neutral-500">Estimated from your own home charges and your off-peak hours. Advice only: Rivolt never changes charging on the truck.</p>
      </div>
    </Card>
  );
}
