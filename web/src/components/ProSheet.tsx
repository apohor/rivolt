import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { PRO } from "../lib/proCopy";
import { daysLeft, planDate, type PlanState } from "../lib/plan";

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50";
const solid = "rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50";

// The renewal sentence for a choice, with the price and period filled in (B1/B2): shown beside the button and tied to its checkbox.
export function consentText(interval: "month" | "year"): string {
  const p = interval === "month" ? PRO.monthly : PRO.yearly;
  return PRO.consent.replace("{period}", p.period).replace("{price}", p.price);
}

// Where the first charge happens, in a sentence, for the account's state (B1): after the trial, or today.
export function firstChargeLine(state: PlanState): string {
  if (state.kind === "trial") {
    const d = planDate(state.ends);
    return d ? `Your trial runs until ${d}. ${PRO.webTrialNoCard} If you subscribe now, your first payment is today.` : PRO.webTrialNoCard;
  }
  return "Your first payment is today, when you subscribe.";
}

// The Pro sheet on the web: what is included and the price before the button, the renewal terms and an unticked consent beside the
// button, links to Terms and Privacy, and "Manage subscription" for someone already subscribed. It opens Stripe Checkout (hosted) and
// never takes a card here. Mocked until billing exists: a failure says so and nothing is charged.
export function ProSheet({ state, onClose }: { state: PlanState; onClose: () => void }) {
  const [interval, setInterval] = useState<"month" | "year">("year");
  const [agreed, setAgreed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    closeRef.current?.focus();
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  async function open(fn: () => Promise<{ url: string }>) {
    setBusy(true);
    setMsg(null);
    try {
      const { url } = await fn();
      window.location.assign(url);
    } catch {
      setMsg("Billing isn't available yet. Nothing was charged.");
    } finally {
      setBusy(false);
    }
  }

  const subscribed = state.kind === "pro" && (state.source === "stripe" || state.source === "apple");
  return createPortal(
    <div className="fixed inset-0 z-[1200] flex items-end justify-center bg-black/60 p-0 sm:items-center sm:p-4" onClick={onClose}>
      <div role="dialog" aria-modal="true" aria-labelledby="pro-title" data-pro-sheet className="max-h-[92vh] w-full max-w-md space-y-4 overflow-y-auto rounded-t-xl border border-neutral-800 bg-neutral-950 p-5 sm:rounded-xl" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 id="pro-title" className="text-lg font-semibold text-neutral-50">{PRO.title}</h2>
            <p className="mt-1 text-sm text-neutral-400">{PRO.subtitle}</p>
          </div>
          <button ref={closeRef} type="button" className={btn} onClick={onClose}>Close</button>
        </div>

        <fieldset className="grid grid-cols-2 gap-2">
          <legend className="sr-only">Plan</legend>
          {([PRO.monthly, PRO.yearly] as const).map((p) => {
            const v = p === PRO.monthly ? "month" : "year";
            return (
              <label key={v} className={`cursor-pointer rounded-lg border p-3 text-sm ${interval === v ? "border-emerald-500 bg-emerald-950/30" : "border-neutral-700"}`}>
                <input type="radio" name="pro-interval" className="sr-only" checked={interval === v} onChange={() => setInterval(v)} />
                <span className="block text-neutral-300">{p.label}</span>
                <span className="block text-base font-semibold text-neutral-50">{p.price} <span className="text-xs font-normal text-neutral-400">/ {p.period}</span></span>
              </label>
            );
          })}
        </fieldset>

        <div>
          <h3 className="text-sm font-semibold text-neutral-200">What's included</h3>
          <ul className="mt-1 list-disc space-y-1 pl-5 text-sm text-neutral-300">
            {PRO.included.map((x) => <li key={x}>{x}</li>)}
          </ul>
          <p className="mt-2 text-xs text-neutral-500">{PRO.keepsFree}</p>
        </div>

        {subscribed ? (
          <div className="space-y-2">
            <p className="text-sm text-neutral-300">You're subscribed{state.kind === "pro" && state.periodEnd ? `; the current period ends ${planDate(state.periodEnd)}` : ""}.</p>
            {state.kind === "pro" && state.source === "stripe" ? (
              <button type="button" className={btn} disabled={busy} onClick={() => void open(() => backend.billingPortal())}>{PRO.manageStripe}</button>
            ) : (
              <p className="text-sm text-neutral-400">{PRO.manageApple}</p>
            )}
          </div>
        ) : (
          <div className="space-y-2">
            <p className="text-xs text-neutral-400">{firstChargeLine(state)}</p>
            <label className="flex items-start gap-2 text-sm text-neutral-300">
              <input type="checkbox" checked={agreed} onChange={(e) => setAgreed(e.target.checked)} className="mt-0.5" />
              <span>{consentText(interval)}</span>
            </label>
            <button type="button" className={solid} disabled={!agreed || busy} onClick={() => void open(() => backend.billingCheckout(interval))}>
              {PRO.checkoutButton}
            </button>
          </div>
        )}
        {msg && <p role="status" className="text-xs text-amber-300">{msg}</p>}
        <p className="text-xs text-neutral-500">
          <Link to="/terms" className="underline underline-offset-2">{PRO.terms}</Link> · <Link to="/privacy" className="underline underline-offset-2">{PRO.privacy}</Link> · Unofficial, not affiliated with Rivian.
        </p>
      </div>
    </div>,
    document.body,
  );
}

// "3 days left" for the trial line of the Settings card.
export function trialLeft(ends: string, now: Date = new Date()): string {
  const n = daysLeft(ends, now);
  return n == null ? "" : n === 0 ? "ends today" : `${n} ${n === 1 ? "day" : "days"} left`;
}
