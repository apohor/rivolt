import type { ReactNode } from "react";
import { RIDB_CREDIT } from "./stays";

// RidbCredit is the CC BY credit for Recreation.gov (RIDB) data. Shown wherever a campground from it is.
export function RidbCredit({ className = "text-xs text-neutral-500" }: { className?: string }) {
  const c = RIDB_CREDIT;
  return (
    <p data-ridb-credit className={className}>
      {c.lead}
      <a href={c.source.href} target="_blank" rel="noopener noreferrer" className="underline hover:text-neutral-300">{c.source.label}</a>,{" "}
      <a href={c.licence.href} target="_blank" rel="noopener noreferrer" className="underline hover:text-neutral-300">{c.licence.label}</a>
      {c.tail}
    </p>
  );
}

// StayCard is one place to sleep: a hotel or a campground from the suggestions list. The own-stop
// card and the overnight row say the same things about the hookup through plan/hookups.ts.
export function StayCard({
  name,
  kind,
  lines,
  warning,
  link,
  sample,
  action,
}: {
  name: string;
  kind: "hotel" | "campground";
  lines: ReactNode[];
  warning?: string;
  link?: string;
  sample?: boolean;
  action: ReactNode;
}) {
  return (
    <li data-stay-card={kind} className="flex items-start gap-3">
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="text-neutral-100">
          {name}
          {kind === "campground" && <span className="text-neutral-400"> (campground)</span>}
          {sample && <span className="ml-1.5 rounded bg-neutral-800 px-1.5 py-0.5 text-[11px] text-neutral-300">sample</span>}
        </div>
        {lines.filter(Boolean).map((l, i) => (
          <div key={i} className="text-xs text-neutral-400">{l}</div>
        ))}
        {warning && <div data-stay-warning className="text-xs text-amber-400">{warning}</div>}
        {link && (
          <a href={link} target="_blank" rel="noopener noreferrer" className="text-xs text-emerald-400 underline">
            on Recreation.gov
          </a>
        )}
      </div>
      {action}
    </li>
  );
}
