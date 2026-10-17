import { useState } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Card } from "./ui";
import { formatMoney } from "../lib/ledger";

const DISMISS_KEY = "rivolt.firstDay.dismissed";

function readDismissed(): boolean {
  try {
    return window.localStorage.getItem(DISMISS_KEY) === "1";
  } catch {
    return false;
  }
}

// "Your first day" (new look): shown on Now while a connected truck has no drives or charges yet,
// so the first screen says what to do next instead of sitting empty. The caller decides when the
// history is empty; this owns the dismissal and which set-up steps are already done.
export default function FirstDayCard({ vehicleID }: { vehicleID: string }) {
  const [dismissed, setDismissed] = useState(readDismissed);
  const charging = useQuery({ queryKey: ["charging-settings"], queryFn: () => backend.getChargingSettings(), staleTime: 60_000 });
  const lease = useQuery({ queryKey: ["lease", "terms", vehicleID], queryFn: () => backend.leaseGet(vehicleID), staleTime: 60_000, retry: 0 });
  if (dismissed || charging.isPending || lease.isPending) return null;
  const rateDone = (charging.data?.home_price_per_kwh ?? 0) > 0;
  const leaseDone = !!lease.data;
  function dismiss() {
    setDismissed(true);
    try {
      window.localStorage.setItem(DISMISS_KEY, "1");
    } catch {
      /* private mode etc. - hides for this visit only */
    }
  }
  const rate = charging.data?.home_price_per_kwh ?? 0;
  const cur = charging.data?.home_currency || "USD";
  const rateText = `${formatMoney(rate, cur, Math.abs(rate * 100 - Math.round(rate * 100)) > 1e-9 ? 3 : 2)}/kWh`;
  return (
    <Card>
      <div data-first-day className="space-y-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-neutral-100">Your first day with Rivolt</h2>
            <p className="mt-1 text-sm text-neutral-400">
              Rivolt records drives and charges as you use the truck. Two quick settings make the first ones useful.
            </p>
          </div>
          <button type="button" onClick={dismiss} className="shrink-0 text-xs text-neutral-400 hover:text-neutral-200">
            Hide this
          </button>
        </div>
        <ul className="space-y-2">
          <Step
            done={rateDone}
            to="/settings?tab=charging#cost"
            label={rateDone ? `Home rate set: ${rateText}` : "Set your home rate"}
            hint={rateDone ? "" : "So home charges are priced as they're recorded. Charges recorded before you set a rate stay unpriced; setting a rate doesn't change them, but you can add a price to each one."}
            cta="Set home rate"
            doneCta="Change"
          />
          <Step
            done={leaseDone}
            to="/settings#lease"
            label={leaseDone ? "Lease added" : "Add your lease"}
            hint={leaseDone ? "" : "Optional. Enter your allowance and term to see projected miles at lease return and the overage cost."}
            cta="Add lease"
            doneCta="Edit"
          />
        </ul>
      </div>
    </Card>
  );
}

function Step({ done, to, label, hint, cta, doneCta }: { done: boolean; to: string; label: string; hint: string; cta: string; doneCta: string }) {
  return (
    <li data-first-day-step={done ? "done" : "todo"} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-md border border-neutral-800 px-3 py-2">
      <div className="min-w-[180px] flex-1">
        <div className="text-sm text-neutral-200">
          {done && <span aria-hidden className="mr-1.5 text-emerald-400">✓</span>}
          {label}
        </div>
        {hint && <div className="text-xs text-neutral-400">{hint}</div>}
      </div>
      {done ? (
        <Link to={to} className="text-sm text-neutral-300 underline underline-offset-4 hover:text-neutral-100">
          {doneCta}
        </Link>
      ) : (
        <Link to={to} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500">
          {cta}
        </Link>
      )}
    </li>
  );
}
