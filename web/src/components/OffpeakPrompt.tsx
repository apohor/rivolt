import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { appPath, inDemo } from "../lib/demo";

// Money's one-time nudge toward the off-peak savings card: shown to an account that has a home rate but no off-peak hours, and has not said
// its electricity is one flat rate. Dismissible once ("Not now"), remembered on this device; it goes away on its own once hours are saved.
// Growth's words, DA-checked.
export const OFFPEAK_PROMPT_KEY = "rivolt.offpeakPrompt.dismissed";
export const OFFPEAK_PROMPT_TITLE = "Do you have cheaper hours?";
export const OFFPEAK_PROMPT_BODY =
  "Enter your utility's off-peak windows and rate, and Money shows what moving your home charging into them could have saved, as an estimate from your own charges.";

const readDismissed = (): boolean => {
  try {
    return window.localStorage.getItem(OFFPEAK_PROMPT_KEY) === "1";
  } catch {
    return false;
  }
};

// Whether the prompt applies, from the off-peak answer and the home rate. Pure so a test can read every case.
export function offpeakPromptApplies(o: { configured?: boolean; flat_rate?: boolean } | undefined, homeRateSet: boolean): boolean {
  return !!o && homeRateSet && o.configured === false && o.flat_rate !== true;
}

export function OffpeakPrompt({ homeRateSet }: { homeRateSet: boolean }) {
  const [dismissed, setDismissed] = useState(readDismissed);
  const q = useQuery({
    queryKey: ["offpeak"],
    queryFn: () => backend.getOffpeak(),
    enabled: homeRateSet && !dismissed && !inDemo(),
    staleTime: 60_000,
    retry: 0,
  });
  if (dismissed || inDemo() || !offpeakPromptApplies(q.data, homeRateSet)) return null;
  function later() {
    setDismissed(true);
    try {
      window.localStorage.setItem(OFFPEAK_PROMPT_KEY, "1");
    } catch {
      /* blocked storage: hidden for this visit only */
    }
  }
  return (
    <section aria-label={OFFPEAK_PROMPT_TITLE} data-offpeak-prompt className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-4">
      <h2 className="text-sm font-semibold text-neutral-100">{OFFPEAK_PROMPT_TITLE}</h2>
      <p className="mt-1 text-sm text-neutral-400">{OFFPEAK_PROMPT_BODY}</p>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <Link
          to={appPath("/settings?tab=charging#offpeak")}
          className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-emerald-500"
        >
          Set off-peak hours
        </Link>
        <button type="button" onClick={later} className="text-sm text-neutral-400 hover:text-neutral-200">
          Not now
        </button>
      </div>
    </section>
  );
}
