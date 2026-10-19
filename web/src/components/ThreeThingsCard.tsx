import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { track } from "../lib/track";
import { appPath } from "../lib/demo";
import { Card } from "./ui";

const DISMISS_KEY = "rivolt.threeThings.dismissed";

function readDismissed(): boolean {
  try {
    return window.localStorage.getItem(DISMISS_KEY) === "1";
  } catch {
    return false;
  }
}

// iPhone and iPad only offer push to an app that has been added to the home screen.
const iosNotInstalled = (): boolean =>
  typeof navigator !== "undefined" && /iPad|iPhone|iPod/.test(navigator.userAgent) && !(navigator as Navigator & { standalone?: boolean }).standalone;

type Row = { key: string; label: string; hint: string; to: string; cta: string };

// "Three things you may not have turned on" (new look, Now): the charging-finished notification, the lease tracker
// and the monthly statement email, each with a link to where it is turned on. A thing the account already has is
// left out, the statement row only shows where the server offers the email, and the card goes away once there is
// nothing left, or when hidden.
// In the demo (`demo`) all three rows show, with no account calls; Hide lasts for the visit.
export default function ThreeThingsCard({ vehicleID, demo = false }: { vehicleID: string; demo?: boolean }) {
  const [dismissed, setDismissed] = useState(() => !demo && readDismissed());
  const push = useQuery({ queryKey: ["push-status"], queryFn: () => backend.pushStatus(), retry: 0, staleTime: 60_000, enabled: !demo });
  const lease = useQuery({ queryKey: ["lease", "terms", vehicleID], queryFn: () => backend.leaseGet(vehicleID), retry: 0, staleTime: 60_000, enabled: !demo });
  const statement = useQuery({ queryKey: ["settings", "statement-email"], queryFn: () => backend.statementEmailGet(), retry: 0, staleTime: 60_000, enabled: !demo });
  const ready = demo || (!push.isPending && !lease.isPending && !statement.isPending);

  const rows: Row[] = [];
  // Push counts only where the server can send it at all; a subscribed device on the account is "on".
  if (demo || (push.data?.enabled && (push.data.subscription_count ?? 0) === 0)) {
    rows.push({
      key: "push",
      label: "Charge-done notifications",
      hint: iosNotInstalled() ? "On iPhone, add Rivolt to your home screen first (Share → Add to Home Screen)." : "A notification when charging finishes.",
      to: "/settings?tab=notifications",
      cta: "Turn on",
    });
  }
  if (demo || !lease.data) {
    rows.push({ key: "lease", label: "Lease tracker", hint: "Enter your allowance and term to see projected miles at lease return and the overage cost.", to: "/settings#lease", cta: "Add lease" });
  }
  if (demo || (statement.data?.available && !statement.data.enabled)) {
    rows.push({ key: "statement", label: "Monthly statement by email", hint: "On the 1st of each month, last month's cost, split and biggest charges.", to: "/settings?tab=notifications", cta: "Turn on" });
  }

  const shown = ready && !dismissed && rows.length > 0;
  const announced = useRef(false);
  useEffect(() => {
    if (shown && !demo && !announced.current) {
      announced.current = true;
      track("alert_shown", { kind: "three_things" });
    }
  }, [shown, demo]);
  if (!shown) return null;

  function hide() {
    setDismissed(true);
    if (demo) return;
    track("alert_dismiss", { kind: "three_things" });
    try {
      window.localStorage.setItem(DISMISS_KEY, "1");
    } catch {
      /* private mode etc. - hides for this visit only */
    }
  }
  return (
    <Card>
      <div data-three-things className="space-y-3">
        <div className="flex items-start justify-between gap-3">
          <h2 className="text-base font-semibold text-neutral-100">Three things you may not have turned on</h2>
          <button type="button" onClick={hide} className="shrink-0 text-xs text-neutral-400 hover:text-neutral-200">
            Hide this
          </button>
        </div>
        <ul className="space-y-2">
          {rows.map((r) => (
            <li key={r.key} data-three-things-row={r.key} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-md border border-neutral-800 px-3 py-2">
              <div className="min-w-[180px] flex-1">
                <div className="text-sm text-neutral-200">{r.label}</div>
                <div className="text-xs text-neutral-400">{r.hint}</div>
              </div>
              <Link to={appPath(r.to)} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500">
                {r.cta}
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </Card>
  );
}
