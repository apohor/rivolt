import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { planDate } from "../lib/plan";
import { money, REFUND } from "../lib/proCopy";

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-60";

// Settings → Account → Plan, web Pro only: within 14 days of a payment, one account-wide refund of that payment in full that also ends Pro.
// The server decides (GET /api/billing/refund); a 404 or any error means no offer here. `onDone` hands the message to the card, which outlives
// this block: once Pro has ended the block is gone.
export function RefundBlock({ onDone }: { onDone: (message: string) => void }) {
  const qc = useQueryClient();
  const offer = useQuery({ queryKey: ["billing", "refund"], queryFn: () => backend.billingRefundOffer(), retry: false, staleTime: 60_000 });
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const o = offer.data;
  if (!o || !o.eligible) return null;
  const amount = money(o.amount_cents, o.currency);
  const refund = () => {
    setBusy(true);
    setProblem(null);
    backend
      .billingRefund()
      .then((r) => {
        onDone(REFUND.done(money(r.amount_cents, r.currency)));
        void qc.invalidateQueries({ queryKey: ["auth", "me"] });
        void qc.invalidateQueries({ queryKey: ["billing", "refund"] });
      })
      .catch((e: { status?: number }) => {
        setConfirming(false);
        setProblem(e?.status === 409 ? REFUND.gone : REFUND.failed);
        if (e?.status === 409) void qc.invalidateQueries({ queryKey: ["billing", "refund"] });
      })
      .finally(() => setBusy(false));
  };
  return (
    <div data-plan-refund className="space-y-2 border-t border-neutral-800 pt-2">
      <p className="text-sm text-neutral-300">{REFUND.offer(amount, planDate(o.until))}</p>
      {!confirming ? (
        <button type="button" className={btn} onClick={() => setConfirming(true)}>{REFUND.action}</button>
      ) : (
        <div className="space-y-2">
          <p className="text-sm font-medium text-neutral-100">{REFUND.confirm(amount)}</p>
          <div className="flex gap-2">
            <button type="button" className={btn} disabled={busy} onClick={refund}>{REFUND.yes}</button>
            <button type="button" className={btn} disabled={busy} onClick={() => setConfirming(false)}>{REFUND.keep}</button>
          </div>
        </div>
      )}
      {problem && <p role="status" className="text-xs text-amber-300">{problem}</p>}
    </div>
  );
}
