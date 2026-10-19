// One line under the period header when Now or Activity is following the last 30 days because the chosen month has nothing
// in it yet (early in the month). The period picker still picks a month.
export const EARLY_MONTH_NOTE = "Nothing recorded yet this month, so this shows the last 30 days.";

export const EARLY_MONTH_TAG = "Last 30 days";

// The month was chosen explicitly (the picker stays visible) and is empty; no auto-switch.
export const emptyMonthLine = (month: string) => `No drives or charges in ${month}.`;

// A brand-new account: nothing in the last 30 days either. Now leaves this to the first-day card.
export const NEW_ACCOUNT_LINE = "Nothing recorded yet. Drives and charges appear here as you use the truck.";

export function EarlyMonthNote() {
  return (
    <p role="note" data-early-month className="text-sm text-neutral-400">
      {EARLY_MONTH_NOTE}
    </p>
  );
}
