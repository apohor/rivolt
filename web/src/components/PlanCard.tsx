import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { planState } from "../lib/plan";
import { checkoutMessage, checkoutReturn, POLL_EVERY_MS, POLL_TIMES, withoutCheckout, type CheckoutReturn } from "../lib/checkoutReturn";
import { planCardCopy, PRO } from "../lib/proCopy";
import { Card } from "./ui";
import { ProSheet } from "./ProSheet";

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500";

// Settings → Account → Plan: where the account stands and the one next step. Shown only where the server bills; with billing off (or
// the demo) the account has everything and nothing here appears. Nothing is deleted when Pro ends: the card says so.
export function PlanCard() {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  // Back from Stripe: read ?checkout= once, drop it from the address, and ask for the plan a few times (the webhook sets it).
  const [back, setBack] = useState<CheckoutReturn>(null);
  const [tries, setTries] = useState(0);
  useEffect(() => {
    const r = checkoutReturn(window.location.search);
    if (!r) return;
    setBack(r);
    window.history.replaceState(window.history.state, "", withoutCheckout(window.location.href));
  }, []);
  const proIsOn = me.data?.plan === "pro";
  useEffect(() => {
    if (back !== "success" || proIsOn || tries >= POLL_TIMES) return;
    const t = window.setTimeout(() => {
      setTries((n) => n + 1);
      void qc.invalidateQueries({ queryKey: ["auth", "me"] });
    }, POLL_EVERY_MS);
    return () => window.clearTimeout(t);
  }, [back, proIsOn, tries, qc]);
  const state = planState(me.data);
  const copy = planCardCopy(state);
  // Billing switched off (a rollback) while a Stripe subscription is live: the person can still change or cancel it, and nothing else about
  // plans appears.
  if (!copy) return me.data?.manage_subscription ? <ManageOnlyCard /> : null;
  return (
    <Card title="Plan" id="plan">
      <div data-plan-card={state.kind} className="space-y-2">
        {back && (
          <p role="status" data-checkout-return={back} className="text-sm text-emerald-300">
            {checkoutMessage(back, proIsOn, tries >= POLL_TIMES)}
          </p>
        )}
        <p className="text-sm font-medium text-neutral-100">{copy.title}</p>
        <p className="text-sm text-neutral-300">{copy.line}</p>
        {copy.action && <button type="button" className={btn} onClick={() => setOpen(true)}>{copy.action}</button>}
      </div>
      {open && <ProSheet state={state} onClose={() => setOpen(false)} />}
    </Card>
  );
}

function ManageOnlyCard() {
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  return (
    <Card title="Plan" id="plan">
      <div data-plan-card="manage-only" className="space-y-2">
        <p className="text-sm font-medium text-neutral-100">Rivolt Pro</p>
        <p className="text-sm text-neutral-300">Billed through Stripe. New purchases are switched off, but you can still change the plan, update the card or cancel.</p>
        <button
          type="button"
          className={btn}
          disabled={busy}
          onClick={() => {
            setBusy(true);
            setProblem(null);
            backend
              .billingPortal()
              .then(({ url }) => window.location.assign(url))
              .catch(() => setProblem(PRO.billingUnavailable))
              .finally(() => setBusy(false));
          }}
        >
          {PRO.manageStripe}
        </button>
        {problem && <p role="status" className="text-xs text-amber-300">{problem}</p>}
      </div>
    </Card>
  );
}
