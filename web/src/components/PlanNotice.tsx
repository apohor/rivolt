import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { planDate, planState } from "../lib/plan";

const KEY = "rivolt.planNotice.dismissed";
const dismissedFor = (id: string): boolean => {
  try {
    return window.localStorage.getItem(KEY) === id;
  } catch {
    return false;
  }
};

// The lapse notice, in the app's header area: Pro ended, the free window applies, nothing is deleted, the export stays free. Only for a
// lapsed account on a server that bills; one dismissal per lapse, remembered in the browser.
export function PlanNotice({ now }: { now?: Date }) {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const state = planState(me.data, now);
  const id = state.kind === "lapsed" ? (state.endedOn ?? "lapsed") : "";
  const [gone, setGone] = useState(() => (id ? dismissedFor(id) : false));
  if (state.kind !== "lapsed" || gone) return null;
  const on = planDate(state.endedOn);
  return (
    <div role="note" data-plan-notice className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-sky-800/60 bg-sky-950/40 px-3 py-2 text-xs text-sky-100">
      <span>
        Pro ended{on ? ` on ${on}` : ""}. Now and Activity show the last 90 days. Nothing is deleted, and you can export everything any time.
      </span>
      <span className="flex gap-3">
        <Link to="/settings#export" className="underline underline-offset-2">Export</Link>
        <Link to="/settings#plan" className="underline underline-offset-2">See Pro</Link>
        <button
          type="button"
          className="text-sky-300 hover:text-sky-100"
          onClick={() => {
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
    </div>
  );
}
