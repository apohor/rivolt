import type { TripCostStop } from "../lib/api";
import { NO_PRICE_TEXT, safeTrackerUrl, stopCostText } from "../lib/planCost";

// Where a stop's price comes from, in the words the server sends (exact text; never "listed", "official", "current" or
// "network rate"), or "Price shown at the charger." when there is none. With a membership price the two ends say which is which.
export function StopPriceNote({ stop, money, className = "" }: { stop: TripCostStop | undefined; money: (v: number) => string; className?: string }) {
  if (!stop) return null;
  const p = stop.price;
  if (!p) {
    return (
      <p data-price-note="none" className={className}>
        {NO_PRICE_TEXT}
      </p>
    );
  }
  const ends =
    p.low_note && p.high_note && stop.cost_low != null && stop.cost_high != null && stop.cost_low !== stop.cost_high
      ? `${money(stop.cost_low)} ${p.low_note} · ${money(stop.cost_high)} ${p.high_note}`
      : null;
  return (
    <div data-price-note={p.basis} className={className}>
      <p>
        {p.source && safeTrackerUrl(p.source_url) && p.label.includes(p.source) ? (
          <>
            {p.label.split(p.source)[0]}
            <a href={safeTrackerUrl(p.source_url) as string} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-200">
              {p.source}
            </a>
            {p.label.split(p.source).slice(1).join(p.source)}
          </>
        ) : (
          p.label
        )}
      </p>
      {ends && <p>{ends}</p>}
    </div>
  );
}

export const stopCostOrDash = (s: TripCostStop | undefined, money: (v: number) => string): string => stopCostText(s, money) ?? "—";
