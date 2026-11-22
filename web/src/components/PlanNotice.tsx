import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { useDesignPreview } from "../lib/config";
import { useProPrompt } from "../lib/proPrompts";
import { planState } from "../lib/plan";
import { graceBanner, lapseBanner, PRO } from "../lib/proCopy";
import { ProSheet, ProSheetPreload } from "./ProSheetLazy";

const KEY = "rivolt.planNotice.dismissed";
const forget = () => {
  try {
    window.localStorage.removeItem(KEY);
  } catch {
    /* nothing to forget */
  }
};
const dismissedFor = (id: string): boolean => {
  try {
    return window.localStorage.getItem(KEY) === id;
  } catch {
    return false;
  }
};

// The lapse banner, in the app's header area: Pro ended, the free window applies, nothing is deleted, the export stays free. Only for
// a lapsed account on a server that bills; one dismissal per lapse, remembered in the browser.
export function PlanNotice() {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const state = planState(me.data);
  const id = state.kind === "lapsed" ? (state.endedOn ?? "lapsed") : "";
  const [gone, setGone] = useState(() => (id ? dismissedFor(id) : false));
  // pro_entry (D6): the dismissal is kept per account on the server, and a dismissal already saved in this browser counts once as the first "Not now".
  const entry = useDesignPreview("pro_entry");
  const store = useProPrompt("lapse_banner", entry ? me.data : undefined);
  const moved = useRef(false);
  const savedHere = entry && !!id && dismissedFor(id);
  useEffect(() => {
    if (!entry || !savedHere || moved.current || !me.data) return;
    moved.current = true;
    forget();
    if (!store.hidden) void store.dismiss();
  }, [entry, savedHere, me.data, store]);
  const [open, setOpen] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  // A failed payment inside the 7-day grace: not dismissible (it needs doing), and Pro stays on while Stripe retries.
  const grace = graceBanner(state);
  if (grace) {
    return (
      <div role="alert" data-plan-notice="grace" className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-amber-800/60 bg-amber-950/40 px-3 py-2 text-xs text-amber-100">
        <span>{grace}</span>
        <button
          type="button"
          className="underline underline-offset-2"
          onClick={() => {
            setProblem(null);
            backend.billingPortal().then(({ url }) => window.location.assign(url)).catch(() => setProblem(PRO.billingUnavailable));
          }}
        >
          Update card
        </button>
        {problem && <span role="status" className="basis-full text-amber-300">{problem}</span>}
      </div>
    );
  }
  if (state.kind !== "lapsed" || (entry ? store.hidden || savedHere : gone)) return null;
  return (
    <div role="note" data-plan-notice className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-sky-800/60 bg-sky-950/40 px-3 py-2 text-xs text-sky-100">
      <span>{lapseBanner(state)}</span>
      <span className="flex gap-3">
        <ProSheetPreload />
        <button type="button" className="underline underline-offset-2" onClick={() => setOpen(true)}>Keep Pro</button>
        <Link to="/settings#export" className="underline underline-offset-2">Export everything</Link>
        <button
          type="button"
          className="text-sky-300 hover:text-sky-100"
          onClick={() => {
            if (entry) {
              void store.dismiss();
              return;
            }
            setGone(true);
            try {
              window.localStorage.setItem(KEY, id);
            } catch {
              /* hides for this visit only */
            }
          }}
        >
          Dismiss
        </button>
      </span>
      {open && <ProSheet state={state} onClose={() => setOpen(false)} />}
    </div>
  );
}
