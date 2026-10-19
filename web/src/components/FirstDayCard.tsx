import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Card } from "./ui";
import { formatMoney } from "../lib/ledger";
import { pushSupported, subscribeThisDevice } from "../lib/push";

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
              Rivolt records drives and charges as you use the truck. A few quick settings make the first ones useful.
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
          <UpdateAlertsRow />
        </ul>
      </div>
    </Card>
  );
}

const ALERTS_DISMISS_KEY = "rivolt.firstDay.alertsDismissed";

// "Update alerts": opt in to software-update push on this device, at the moment it is useful. The
// browser's permission prompt only appears on the tap. Hidden when the browser or server can't push,
// or after "Not now" (not asked again here).
function UpdateAlertsRow() {
  const status = useQuery({ queryKey: ["push-status"], queryFn: () => backend.pushStatus(), retry: 0, staleTime: 60_000 });
  const [state, setState] = useState<"idle" | "busy" | "on" | "blocked" | "error">(() =>
    pushSupported() && Notification.permission === "denied" ? "blocked" : "idle",
  );
  const [subscribed, setSubscribed] = useState(false);
  // The tap is held until we know whether this device is already subscribed.
  const [checked, setChecked] = useState(false);
  const [hidden, setHidden] = useState(() => {
    try {
      return window.localStorage.getItem(ALERTS_DISMISS_KEY) === "1";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    if (!pushSupported()) return;
    void navigator.serviceWorker.ready
      .then((reg) => reg.pushManager.getSubscription())
      .then((s) => setSubscribed(!!s))
      .catch(() => undefined)
      .finally(() => setChecked(true));
  }, []);
  if (!pushSupported() || !status.data?.enabled) return null;
  const on = state === "on" || (subscribed && state !== "blocked");
  if (hidden && !on) return null;
  async function enable() {
    setState("busy");
    try {
      const r = await subscribeThisDevice({ on_charging_done: false, on_plug_in_reminder: false, on_anomaly: false, on_software_update: true, on_milestones: false }, { leaveExisting: true });
      setState(r === "on" ? "on" : "blocked");
    } catch {
      setState("error");
    }
  }
  function notNow() {
    setHidden(true);
    try {
      window.localStorage.setItem(ALERTS_DISMISS_KEY, "1");
    } catch {
      /* private mode etc. - hides for this visit only */
    }
  }
  return (
    <li data-first-day-step={on ? "done" : "todo"} data-first-day-alerts className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-md border border-neutral-800 px-3 py-2">
      <div className="min-w-[180px] flex-1">
        <div className="text-sm text-neutral-200">
          {on && <span aria-hidden className="mr-1.5 text-emerald-400">✓</span>}
          {on ? "Alerts on" : "Update alerts"}
        </div>
        {!on && (
          <div className="text-xs text-neutral-400">
            {state === "blocked"
              ? "Notifications are blocked in this browser. You can allow them in the browser's site settings."
              : "Get a notification when a software update is offered or installs."}
          </div>
        )}
        {state === "error" && (
          <div role="alert" className="text-xs text-rose-400">
            Couldn&apos;t turn alerts on. Try again in a moment.
          </div>
        )}
      </div>
      {!on && state !== "blocked" && (
        <div className="flex items-center gap-3">
          <button type="button" onClick={notNow} className="text-xs text-neutral-400 hover:text-neutral-200">
            Not now
          </button>
          <button
            type="button"
            onClick={enable}
            disabled={state === "busy" || !checked}
            className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50"
          >
            Turn on alerts
          </button>
        </div>
      )}
    </li>
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
