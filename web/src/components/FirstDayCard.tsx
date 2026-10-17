import { useState } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Card } from "./ui";

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
  return (
    <Card>
      <div data-first-day className="space-y-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-neutral-100">Your first day</h2>
            <p className="mt-1 text-sm text-neutral-400">
              You&apos;re connected. Drives and charges appear here as you use the truck. Two things worth doing now:
            </p>
          </div>
          <button type="button" onClick={dismiss} className="shrink-0 text-xs text-neutral-400 hover:text-neutral-200">
            Dismiss
          </button>
        </div>
        <ul className="space-y-2">
          <Step done={rateDone} to="/settings?tab=charging#cost" label="Set your home rate" cta="Set rate" hint="So home charging shows what it costs." />
          <Step done={leaseDone} to="/settings#lease" label="Add your lease" cta="Add lease" hint="Track miles against what your lease allows." />
        </ul>
      </div>
    </Card>
  );
}

function Step({ done, to, label, hint, cta }: { done: boolean; to: string; label: string; hint: string; cta: string }) {
  return (
    <li data-first-day-step={done ? "done" : "todo"} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-md border border-neutral-800 px-3 py-2">
      <div className="min-w-[180px] flex-1">
        <div className="text-sm text-neutral-200">
          {done && <span aria-hidden className="mr-1.5 text-emerald-400">✓</span>}
          {label}
          {done && <span className="sr-only"> (done)</span>}
        </div>
        <div className="text-xs text-neutral-400">{hint}</div>
      </div>
      <Link to={to} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500">
        {done ? "Review" : cta}
      </Link>
    </li>
  );
}
