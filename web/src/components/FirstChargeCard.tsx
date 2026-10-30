import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend, type Charge, type Drive, type PushPreferences } from "../lib/api";
import { firstChargeView } from "../lib/firstCharge";
import { formatMoney } from "../lib/ledger";
import { isIOSNotInstalled } from "../lib/installPrompt";
import { InstallOfferRow } from "./InstallOfferRow";
import { pushSupported, subscribeThisDevice } from "../lib/push";
import { appPath } from "../lib/demo";
import { Card } from "./ui";
import { FirstWeekLine } from "./FirstWeekLine";

const DISMISS_KEY = "rivolt.firstCharge.dismissed";
const NOTIFY_HIDE_KEY = "rivolt.firstCharge.notifyHidden";

function flag(key: string): boolean {
  try {
    return window.localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}
function setFlag(key: string) {
  try {
    window.localStorage.setItem(key, "1");
  } catch {
    /* private mode etc. - hides for this visit only */
  }
}

// "Your first charge" (new look): once the first charge, or the first drive, is recorded, the card says what it
// was and what to do next, for a week or until hidden. It takes the place of "Your first day", which is for the
// time before anything is recorded.
// In the demo (`demo`) the sample truck's first charge is shown as a worked example: no account calls, the rate
// is the one that charge was priced at, the week limit does not apply, and Hide lasts for the visit.
export default function FirstChargeCard({ vehicleID, charges, drives, demo = false }: { vehicleID: string; charges: Charge[]; drives: Drive[]; demo?: boolean }) {
  const [dismissed, setDismissed] = useState(() => !demo && flag(DISMISS_KEY));
  const settings = useQuery({ queryKey: ["charging-settings"], queryFn: () => backend.getChargingSettings(), staleTime: 60_000, enabled: !demo });
  const lease = useQuery({ queryKey: ["lease", "terms", vehicleID], queryFn: () => backend.leaseGet(vehicleID), staleTime: 60_000, retry: 0, enabled: !demo });
  if (dismissed || (!demo && settings.isPending)) return null;
  // The demo's example is its first priced charge, whatever came before it.
  const demoCharges = demo ? charges.filter((c) => c.EnergyAddedKWh > 0 && c.Cost > 0) : charges;
  const demoFirst = demo ? [...demoCharges].sort((a, b) => a.StartedAt.localeCompare(b.StartedAt))[0] : undefined;
  if (demo && !demoFirst) return null;
  const homeRate = demoFirst ? demoFirst.Cost / demoFirst.EnergyAddedKWh : (settings.data?.home_price_per_kwh ?? 0);
  const v = firstChargeView({
    charges: demoCharges,
    // The demo's example is its first priced charge, so a drive that came before it must not age the card out.
    drives: demo ? [] : drives,
    homeRate,
    now: demoFirst ? new Date(new Date(demoFirst.StartedAt).getTime() + 86_400_000) : undefined,
  });
  if (!v) return null;
  const currency = settings.data?.home_currency || "USD";
  const hide = () => {
    setDismissed(true);
    if (!demo) setFlag(DISMISS_KEY);
  };
  const rateText = (r: number) => `${formatMoney(r, currency, Math.abs(r * 100 - Math.round(r * 100)) > 1e-9 ? 3 : 2)}/kWh`;
  const link = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500";
  return (
    <Card>
      <div data-first-charge={v.state} className="space-y-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            {v.state === "A" && (
              <>
                <h2 className="text-base font-semibold text-neutral-100">
                  Your first charge: {v.kwh.toFixed(1)} kWh, {formatMoney(v.cost, v.currency)}
                </h2>
                <p className="mt-1 text-sm text-neutral-400">
                  {v.home
                    ? `Priced at your home rate of ${rateText(v.rate)}. Every charge from now on is priced the same way and lands in Money.`
                    : "Every charge from now on is priced and lands in Money."}
                </p>
                {v.perMile && (
                  <p className="mt-1 text-sm text-neutral-400">
                    That charge added about {Math.round(v.perMile.miles)} mi of range (estimate), about {formatMoney(v.perMile.cost, v.currency)}/mi.
                  </p>
                )}
              </>
            )}
            {v.state === "B" && (
              <>
                <h2 className="text-base font-semibold text-neutral-100">Your first charge: {v.kwh.toFixed(1)} kWh</h2>
                <p className="mt-1 text-sm text-neutral-400">Set your home rate and this charge gets a price now; later charges are priced as they arrive.</p>
              </>
            )}
            {v.state === "C" && (
              <>
                <h2 className="text-base font-semibold text-neutral-100">
                  Your first drive: {v.miles.toFixed(1)} mi{v.miPerKwh ? ` at ${v.miPerKwh.toFixed(1)} mi/kWh` : ""}
                </h2>
                <p className="mt-1 text-sm text-neutral-400">Charges appear here as the truck is plugged in; a home rate prices them automatically.</p>
              </>
            )}
          </div>
          <button type="button" onClick={hide} className="shrink-0 text-xs text-neutral-400 hover:text-neutral-200">
            Hide this
          </button>
        </div>
        <div className="flex flex-wrap gap-2">
          {v.state === "A" && (
            <>
              <Link to={appPath(`/charges/${v.chargeID}`)} className={link}>
                See the receipt
              </Link>
              <Link to={appPath("/money")} className={link}>
                Open Money
              </Link>
            </>
          )}
          {v.state === "B" && (
            <Link to="/settings?tab=charging#cost" className={link}>
              Set home rate
            </Link>
          )}
          {v.state === "C" && (
            <>
              <Link to={`/drives/${v.driveID}`} className={link}>
                See the drive
              </Link>
              {homeRate <= 0 && (
                <Link to="/settings?tab=charging#cost" className={link}>
                  Set home rate
                </Link>
              )}
            </>
          )}
        </div>
        {!demo && v.lastDay && <FirstWeekLine />}
        {!demo && (
        <ul className="space-y-2">
          <NotifyRow />
          {!lease.isPending && !lease.data && (
            <li data-first-charge-row="lease" className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-md border border-neutral-800 px-3 py-2">
              <div className="min-w-[180px] flex-1">
                <div className="text-sm text-neutral-200">Add your lease</div>
                <div className="text-xs text-neutral-400">Optional. Enter your allowance and term to see projected miles at lease return and the overage cost.</div>
              </div>
              <Link to="/settings#lease" className={link}>
                Add lease
              </Link>
            </li>
          )}
        </ul>
        )}
      </div>
    </Card>
  );
}

// "Get a notification when charging finishes": opt in on this device at the moment it is useful, from a tap. On an
// iPhone that is not on the home screen yet, say how; where the browser offers to install the app, offer that too.
// A row that is done, or that the user said not now to, goes away.
function NotifyRow() {
  const status = useQuery({ queryKey: ["push-status"], queryFn: () => backend.pushStatus(), retry: 0, staleTime: 60_000 });
  const [state, setState] = useState<"idle" | "busy" | "on" | "blocked" | "error">(() => (pushSupported() && Notification.permission === "denied" ? "blocked" : "idle"));
  const [subscribed, setSubscribed] = useState(false);
  const [checked, setChecked] = useState(false);
  const [hidden, setHidden] = useState(() => flag(NOTIFY_HIDE_KEY));
  useEffect(() => {
    if (!pushSupported()) {
      setChecked(true);
      return;
    }
    void navigator.serviceWorker.ready
      .then((reg) => reg.pushManager.getSubscription())
      .then((s) => setSubscribed(!!s))
      .catch(() => undefined)
      .finally(() => setChecked(true));
  }, []);
  const row = "flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-md border border-neutral-800 px-3 py-2";
  const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50";

  // iPhone, not on the home screen: push is not offered yet. Say how.
  if (!pushSupported() && isIOSNotInstalled()) {
    if (hidden) return null;
    return (
      <li data-first-charge-row="notify-ios" className={row}>
        <div className="min-w-[180px] flex-1">
          <div className="text-sm text-neutral-200">Get a notification when charging finishes</div>
          <div className="text-xs text-neutral-400">On iPhone, add Rivolt to your home screen first (Share → Add to Home Screen), then turn this on here.</div>
        </div>
        <button type="button" onClick={() => { setHidden(true); setFlag(NOTIFY_HIDE_KEY); }} className="text-xs text-neutral-400 hover:text-neutral-200">
          Not now
        </button>
      </li>
    );
  }
  if (!pushSupported() || !status.data?.enabled) {
    // No push here, but a browser that can install the app still gets that offer.
    return <InstallOfferRow />;
  }
  const on = state === "on" || (subscribed && state !== "blocked");
  if (on) return null;
  if (hidden) return <InstallOfferRow />;
  async function turnOn() {
    setState("busy");
    try {
      // Only charging-complete goes on, and a device that is already subscribed keeps the choices it has.
      const prefs: PushPreferences = { on_charging_done: true, on_plug_in_reminder: false, on_anomaly: false, on_software_update: false, on_milestones: false };
      const r = await subscribeThisDevice(prefs, { leaveExisting: true });
      setState(r === "on" ? "on" : "blocked");
    } catch {
      setState("error");
    }
  }
  return (
    <>
      {!hidden && (
        <li data-first-charge-row="notify" className={row}>
          <div className="min-w-[180px] flex-1">
            <div className="text-sm text-neutral-200">Get a notification when charging finishes</div>
            {state === "blocked" && <div className="text-xs text-neutral-400">Notifications are blocked in this browser. You can allow them in the browser&apos;s site settings.</div>}
            {state === "error" && <div role="alert" className="text-xs text-rose-400">Couldn&apos;t turn that on. Try again in a moment.</div>}
          </div>
          {state !== "blocked" && (
            <div className="flex items-center gap-3">
              <button type="button" onClick={() => { setHidden(true); setFlag(NOTIFY_HIDE_KEY); }} className="text-xs text-neutral-400 hover:text-neutral-200">
                Not now
              </button>
              <button type="button" onClick={turnOn} disabled={state === "busy" || !checked} className={btn}>
                Turn on
              </button>
            </div>
          )}
        </li>
      )}
      <InstallOfferRow />
    </>
  );
}
