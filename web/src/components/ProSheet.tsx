import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { DemoLinks } from "./DemoLinks";
import { DEMO_HISTORY, DEMO_PLANNER, DEMO_TRENDS } from "../lib/demoLinks";
import { PRO, checkoutLabel, consentText, type Interval } from "../lib/proCopy";
import { planDate, type PlanState } from "../lib/plan";

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50";
const solid = "rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50";

// The Pro sheet on the web: what is included and the price before the button, two plan choices with nothing pre-selected, the renewal
// terms and an unticked consent directly above the button, links to Terms and Privacy, and "Manage subscription" for someone already
// subscribed. It opens Stripe Checkout (hosted) and never takes a card here. Mocked until billing exists: a failure says so and nothing
// is charged.
export function ProSheet({ state, onClose }: { state: PlanState; onClose: () => void }) {
  const [interval, setInterval] = useState<Interval | null>(null);
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
      setMsg(PRO.billingUnavailable);
    } finally {
      setBusy(false);
    }
  }

  const stripe = state.kind === "pro" && state.source === "stripe";
  const cancelled = state.kind === "pro" && !!state.cancelsAt;
  const failed = state.kind === "pro" && !!state.graceEnds;
  const apple = state.kind === "pro" && state.source === "apple";
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

        <div>
          <h3 className="text-sm font-semibold text-neutral-200">What's included</h3>
          <ul className="mt-1 list-disc space-y-1 pl-5 text-sm text-neutral-300">
            {PRO.included.map((x) => <li key={x}>{x}</li>)}
          </ul>
        </div>

        {!stripe && !apple && <DemoLinks links={[DEMO_PLANNER, DEMO_HISTORY, DEMO_TRENDS]} />}

        {stripe || apple ? (
          <div className="space-y-2">
            <p className="text-sm text-neutral-300">
              {failed
                ? `A payment failed. Pro stays on until ${planDate(state.kind === "pro" ? state.graceEnds : null)}; update your card to keep it.`
                : cancelled
                  ? `You cancelled. Pro stays on until ${planDate(state.kind === "pro" ? state.cancelsAt : null)}, and nothing is deleted after.`
                  : `You're subscribed${state.kind === "pro" && state.periodEnd ? `; the current period ends ${planDate(state.periodEnd)}` : ""}.`}
            </p>
            {stripe ? (
              <>
                <button type="button" className={btn} disabled={busy} onClick={() => void open(() => backend.billingPortal())}>{PRO.manageStripe}</button>
                <p className="text-xs text-neutral-500">{PRO.manageStripeHelp}</p>
              </>
            ) : (
              <p className="text-sm text-neutral-400">{PRO.manageApple}</p>
            )}
          </div>
        ) : (
          <div className="space-y-3">
            <fieldset className="grid grid-cols-2 gap-2">
              <legend className="sr-only">Plan</legend>
              {(["year", "month"] as const).map((v) => {
                const o = PRO.options[v];
                return (
                  <label key={v} className={`cursor-pointer rounded-lg border p-3 text-sm ${interval === v ? "border-emerald-500 bg-emerald-950/30" : "border-neutral-700"}`}>
                    <input type="radio" name="pro-interval" className="sr-only" checked={interval === v} onChange={() => {
                      // The consent names the price and period of the choice, so a new choice needs a new tick.
                      setInterval(v);
                      setAgreed(false);
                    }} />
                    <span className="block text-neutral-300">{o.label}</span>
                    <span className="block text-base font-semibold text-neutral-50">{o.price} <span className="text-xs font-normal text-neutral-400">{o.unit}</span></span>
                    {o.extra && <span className="block text-xs text-neutral-500">{o.extra}</span>}
                  </label>
                );
              })}
            </fieldset>
            {interval && (
              <label className="flex items-start gap-2 text-sm text-neutral-300">
                <input type="checkbox" checked={agreed} onChange={(e) => setAgreed(e.target.checked)} className="mt-0.5" />
                <span>
                  {consentText(interval, state)}{" "}
                  <Link to="/terms" className="underline underline-offset-2">Terms</Link> · <Link to="/privacy" className="underline underline-offset-2">Privacy</Link>
                </span>
              </label>
            )}
            <button type="button" className={solid} disabled={!interval || !agreed || busy} onClick={() => interval && void open(() => backend.billingCheckout(interval))}>
              {interval ? checkoutLabel(interval) : "Choose a plan"}
            </button>
            <p className="text-xs text-neutral-500">{PRO.underButton} <span data-us-billing>{PRO.usBilling}</span></p>
          </div>
        )}
        {msg && <p role="status" className="text-xs text-amber-300">{msg}</p>}
        <p className="text-xs text-neutral-500">
          {stripe || apple ? (<><Link to="/terms" className="underline underline-offset-2">Terms</Link> · <Link to="/privacy" className="underline underline-offset-2">Privacy</Link> · </>) : null}
          {PRO.unofficial}
        </p>
      </div>
    </div>,
    document.body,
  );
}
