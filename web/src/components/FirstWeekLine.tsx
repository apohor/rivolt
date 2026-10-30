import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { appPath } from "../lib/demo";

// The first-charge card's day-7 line (Sprint 12; growth's words, DA-glanced): on the card's last day, a pointer to Money and, with no
// off-peak hours set, to the off-peak setting. It lives on the card, so it shows once (the card goes after the week or when hidden) and
// never returns. No figures in the line itself; the savings card carries them, labelled as an estimate.
export const WEEK_LINE_WITH_SCHEDULE =
  "A week of charges is in. Money has the month so far, with each charge's cost and where it came from, and an estimate of what moving charges into your off-peak hours could have saved.";
export const WEEK_LINE_WITHOUT_SCHEDULE =
  "A week of charges is in. Money has the month so far, with each charge's cost and where it came from. Set your off-peak hours and it also shows an estimate of what moving charges into them could have saved.";

export function FirstWeekLine() {
  const offpeak = useQuery({ queryKey: ["offpeak"], queryFn: () => backend.getOffpeak(), staleTime: 60_000, retry: 0 });
  if (offpeak.isPending) return null;
  const hasSchedule = offpeak.data?.configured === true || offpeak.data?.flat_rate === true;
  const link = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500";
  return (
    <div data-first-week-line className="space-y-2 rounded-md border border-neutral-800 px-3 py-2">
      <p className="text-sm text-neutral-300">{hasSchedule ? WEEK_LINE_WITH_SCHEDULE : WEEK_LINE_WITHOUT_SCHEDULE}</p>
      <div className="flex flex-wrap gap-2">
        <Link to={appPath("/money")} className={link}>
          Open Money
        </Link>
        {!hasSchedule && (
          <Link to="/settings?tab=charging#offpeak" className={link}>
            Set off-peak hours
          </Link>
        )}
      </div>
    </div>
  );
}
