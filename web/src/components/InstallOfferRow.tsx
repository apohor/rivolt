import { useState, useSyncExternalStore } from "react";
import { canInstall, onInstallChange, promptInstall } from "../lib/installPrompt";

// Shared by the first-day and first-charge cards: hiding it in one hides it in both.
const HIDE_KEY = "rivolt.firstCharge.installHidden";

function hiddenBefore(): boolean {
  try {
    return window.localStorage.getItem(HIDE_KEY) === "1";
  } catch {
    return false;
  }
}

// "Add Rivolt to your home screen": only where the browser has offered to install the app (Chromium), only on a tap, and
// gone for good after Not now. Renders nothing otherwise.
export function InstallOfferRow() {
  const installable = useSyncExternalStore(onInstallChange, canInstall, () => false);
  const [hidden, setHidden] = useState(hiddenBefore);
  if (!installable || hidden) return null;
  return (
    <li
      data-first-charge-row="install"
      data-install-row
      className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-md border border-neutral-800 px-3 py-2"
    >
      <div className="min-w-[180px] flex-1 text-sm text-neutral-200">Add Rivolt to your home screen</div>
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={() => {
            setHidden(true);
            try {
              window.localStorage.setItem(HIDE_KEY, "1");
            } catch {
              /* private mode etc. - hides for this visit only */
            }
          }}
          className="text-xs text-neutral-400 hover:text-neutral-200"
        >
          Not now
        </button>
        <button
          type="button"
          onClick={() => void promptInstall()}
          className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500"
        >
          Install
        </button>
      </div>
    </li>
  );
}
