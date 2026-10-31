import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { inDemo } from "../lib/demo";
import { buildYearShareModel, yearCardYear, yearFileName } from "../lib/yearInRivolt";
import { track } from "../lib/track";
import { Card } from "./ui";
import { ShareSheet } from "./ShareSheet";

const HIDE_KEY = "rivolt.yearCard.hidden";
const hiddenFor = (year: number): boolean => {
  try {
    return window.localStorage.getItem(HIDE_KEY) === String(year);
  } catch {
    return false;
  }
};

// "Your year in Rivolt": in January, once the server says this account has a year worth showing. The card is made in the browser and
// nothing is posted. Not in the demo, and nothing is asked of the server outside January.
export function YearCard({ now }: { now?: Date }) {
  const year = inDemo() ? null : yearCardYear(now);
  const q = useQuery({ queryKey: ["year-in-rivolt", year], queryFn: () => backend.yearInRivolt(year ?? undefined), enabled: year != null, staleTime: 3_600_000, retry: 0 });
  const [open, setOpen] = useState(false);
  const [hidden, setHidden] = useState(() => (year != null ? hiddenFor(year) : false));
  if (year == null || hidden || !q.data?.eligible) return null;
  const y = q.data;
  const model = buildYearShareModel(y, { includeCost: false });
  if (!model) return null;
  const hide = () => {
    setHidden(true);
    try {
      window.localStorage.setItem(HIDE_KEY, String(year));
    } catch {
      /* hides for this visit only */
    }
  };
  return (
    <Card>
      <div data-year-card className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-neutral-100">{model.title}</h2>
          <p className="mt-1 text-sm text-neutral-400">
            {model.subtitle}. A card you can share or keep; it is made on your device and nothing is posted.
          </p>
        </div>
        <div className="flex gap-2">
          <button type="button" className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500" onClick={() => setOpen(true)}>
            See your year
          </button>
          <button type="button" className="rounded-md px-3 py-1.5 text-sm text-neutral-400 hover:text-neutral-200" onClick={hide}>
            Hide
          </button>
        </div>
      </div>
      {open && (
        <ShareSheet
          build={(includeCost) => buildYearShareModel(y, { includeCost }) ?? model}
          fileName={yearFileName(year)}
          withCostToggle={!!y.cost}
          onShared={() => track("statement_share", { kind: "png" })}
          onClose={() => setOpen(false)}
        />
      )}
    </Card>
  );
}
