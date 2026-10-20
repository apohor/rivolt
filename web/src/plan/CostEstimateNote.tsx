import { COST_ESTIMATE_NOTE } from "./format";

// The cost-estimate line: the parenthetical stays on one line so "mid-2025" never breaks at the hyphen on a phone.
export function CostEstimateNote({ className = "" }: { className?: string }) {
  const i = COST_ESTIMATE_NOTE.indexOf(" (");
  const head = COST_ESTIMATE_NOTE.slice(0, i);
  const tail = COST_ESTIMATE_NOTE.slice(i + 1);
  return (
    <p data-cost-note className={className}>
      {head} <span className="whitespace-nowrap">{tail}</span>
    </p>
  );
}
