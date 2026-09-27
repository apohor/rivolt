import { useState } from "react";
import { Link } from "react-router-dom";
import { Card } from "./ui";

const DISMISS_KEY = "rivolt.electrafiHint.dismissed";

function readDismissed(): boolean {
  try {
    return window.localStorage.getItem(DISMISS_KEY) === "1";
  } catch {
    return false;
  }
}

// ElectraFiImportHint offers the ElectraFi backfill at the moment it's
// useful: a vehicle is connected but there's no history yet. It used
// to be a step in the onboarding wizard, where it stood between new
// users and connecting their Rivian (and had no vehicle to import
// into yet). The caller decides when the history is empty; this owns
// only the dismissal.
export default function ElectraFiImportHint() {
  const [dismissed, setDismissed] = useState(readDismissed);
  if (dismissed) return null;
  function dismiss() {
    setDismissed(true);
    try {
      window.localStorage.setItem(DISMISS_KEY, "1");
    } catch {
      /* private mode etc. - hides for this visit only */
    }
  }
  return (
    <Card>
      <div className="flex flex-wrap items-center gap-4">
        <div className="flex-1 min-w-[200px]">
          <h2 className="text-base font-semibold text-neutral-100">
            Used ElectraFi before?
          </h2>
          <p className="mt-1 text-sm text-neutral-400">
            New drives and charges show up here as you use the truck. To
            start with your full history, export it from the ElectraFi app
            and import the CSVs.
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-3">
          <button
            type="button"
            onClick={dismiss}
            className="text-xs text-neutral-500 hover:text-neutral-300"
          >
            Dismiss
          </button>
          <Link
            to="/settings?tab=data#import"
            className="rounded-md border border-neutral-700 px-4 py-2 text-sm text-neutral-200 hover:border-neutral-500"
          >
            Import history
          </Link>
        </div>
      </div>
    </Card>
  );
}
