// A one-time nudge on Money to share the statement card (Sprint 12 item 10): shown once to an account with a month to show, dismissible,
// remembered on this device. The card is the masked one "Share statement" makes today: no places or addresses, cost only if added, and
// nothing is posted by Rivolt. No new data and no events. Words: growth's, DA-glanced.
export const SHARE_NUDGE_KEY = "rivolt.shareNudge.dismissed";
export const SHARE_NUDGE_TITLE = "Share what charging cost this month";
export const SHARE_NUDGE_BODY = "Your statement card shows the month's kWh and miles, with cost left out unless you add it. Nothing is posted; you pick where it goes.";
export const SHARE_NUDGE_SHARE = "Share statement";
export const SHARE_NUDGE_LATER = "Not now";

export const readShareNudgeDismissed = (): boolean => {
  try {
    return window.localStorage.getItem(SHARE_NUDGE_KEY) === "1";
  } catch {
    return false;
  }
};

export const rememberShareNudgeDismissed = () => {
  try {
    window.localStorage.setItem(SHARE_NUDGE_KEY, "1");
  } catch {
    /* blocked storage: hidden for this visit only */
  }
};

// Whether the nudge is drawn: not in the demo or with nothing to share, not when the statement email's own "Share it" prompt is up, and not
// once dismissed or used.
export const shareNudgeShows = (o: { sample: boolean; disabled: boolean; prompt: boolean; dismissed: boolean }): boolean =>
  !o.sample && !o.disabled && !o.prompt && !o.dismissed;

export function ShareNudge({ onShare, onLater }: { onShare: () => void; onLater: () => void }) {
  return (
    <section aria-label={SHARE_NUDGE_TITLE} data-share-nudge className="mt-2 w-full basis-full rounded-lg border border-neutral-800 bg-neutral-900/60 p-3">
      <h3 className="text-sm font-semibold text-neutral-100">{SHARE_NUDGE_TITLE}</h3>
      <p className="mt-1 text-sm text-neutral-400">{SHARE_NUDGE_BODY}</p>
      <div className="mt-2 flex flex-wrap items-center gap-3">
        <button type="button" onClick={onShare} className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-emerald-500">
          {SHARE_NUDGE_SHARE}
        </button>
        <button type="button" onClick={onLater} className="text-sm text-neutral-400 hover:text-neutral-200">
          {SHARE_NUDGE_LATER}
        </button>
      </div>
    </section>
  );
}
