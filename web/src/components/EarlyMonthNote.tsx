// One line under the period header when Now or Activity is following the last 30 days because the chosen month has nothing
// in it yet (early in the month). The period picker still picks a month.
export const EARLY_MONTH_NOTE = "Nothing recorded yet this month, so this shows the last 30 days.";

export function EarlyMonthNote() {
  return (
    <p role="note" data-early-month className="text-sm text-neutral-400">
      {EARLY_MONTH_NOTE}
    </p>
  );
}
