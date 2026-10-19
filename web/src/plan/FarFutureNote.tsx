// The note on a plan for a trip more than a month out. The server plans such a
// trip at today's date (the gateway is never asked about a far departure), so
// the stops, chargers and times describe today's network, not the trip date, and
// there is no weather forecast. Wording is growth's; DA's rule: say that, and ask
// for a re-plan within a month of leaving.
export const FAR_FUTURE_NOTE =
  "This trip is more than a month away. Stops, chargers and arrival times reflect today's network and conditions, not the date you leave; no forecast is used. Re-plan within a month of leaving for current chargers and times.";

export function FarFutureNote({ show }: { show?: boolean }) {
  if (!show) return null;
  return (
    <p
      role="note"
      data-far-future-note
      className="rounded-md border border-sky-900/50 bg-sky-950/20 px-3 py-2 text-sm text-neutral-200"
    >
      {FAR_FUTURE_NOTE}
    </p>
  );
}
