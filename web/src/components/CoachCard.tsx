import { useQuery } from "@tanstack/react-query";
import { fetchCoach, coachNeedsText, type CoachBucket, type CoachResponse } from "../lib/savings";
import { formatMoney } from "../lib/ledger";
import { Card } from "./ui";

const EST = <span className="text-neutral-500"> (estimated)</span>;

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-0.5">
      <p className="text-xs uppercase tracking-wide text-neutral-500">{label}</p>
      <div className="text-neutral-200">{children}</div>
    </div>
  );
}

const Needs = ({ need }: { need: string }) => <p className="text-neutral-400">{coachNeedsText(need)}</p>;

function bucketLine(name: string, b: CoachBucket, cur: string) {
  if (b.per_kwh == null) return null;
  return (
    <p key={name}>
      {name}: {formatMoney(b.per_kwh, cur, 3)} per kWh
      {b.estimated && EST}
      <span className="text-xs text-neutral-500"> · {b.sessions} {b.sessions === 1 ? "charge" : "charges"}</span>
    </p>
  );
}

// CoachCard: what a kWh and 100 miles really cost, and what shifting to off-peak would save, from the user's own stored charges and drives.
// Behind the savings_coach design-preview key. Advice only.
export function CoachCard({ area }: { area?: string }) {
  const q = useQuery({ queryKey: ["coach"], queryFn: ({ signal }) => fetchCoach(signal), staleTime: 5 * 60_000, retry: 0 });
  if (q.isError) return null;
  const d: CoachResponse | undefined = q.data;
  const cur = d?.currency || "USD";
  const months = d?.months ?? [];
  return (
    <Card title="What your truck really costs" area={area}>
      <div data-coach className="space-y-3 text-sm">
        {!d ? (
          <p className="text-neutral-500">…</p>
        ) : (
          <>
            {months.length > 0 && <p className="text-xs text-neutral-500">{months[0]}{months.length > 1 && <> to {months[months.length - 1]}</>}</p>}
            <Row label="Cost per kWh">
              {d.cost_per_kwh.needs ? (
                <Needs need={d.cost_per_kwh.needs} />
              ) : (
                <>
                  {bucketLine("Home", d.cost_per_kwh.home, cur)}
                  {bucketLine("Public AC", d.cost_per_kwh.public, cur)}
                  {bucketLine("Fast charging", d.cost_per_kwh.fast_charging, cur)}
                  {d.cost_per_kwh.fast_premium_pct != null && (
                    <p className="text-xs text-neutral-400">Fast charging costs about {Math.round(d.cost_per_kwh.fast_premium_pct)}% more per kWh than home{d.cost_per_kwh.estimated && " (estimated)"}.</p>
                  )}
                </>
              )}
            </Row>
            <Row label="Cost per 100 miles">
              {d.cost_per_100.needs || d.cost_per_100.per_100_mi == null ? (
                <Needs need={d.cost_per_100.needs ?? "more_driving"} />
              ) : (
                <p>
                  {formatMoney(d.cost_per_100.per_100_mi, cur)} per 100 mi
                  {d.cost_per_100.per_100_km != null && <span className="text-neutral-400"> · {formatMoney(d.cost_per_100.per_100_km, cur)} per 100 km</span>}
                  {d.cost_per_100.estimated && EST}
                </p>
              )}
            </Row>
            <Row label="Charging off-peak">
              {d.off_peak_shift.needs || d.off_peak_shift.could_have_saved == null ? (
                <Needs need={d.off_peak_shift.needs ?? "more_home_sessions"} />
              ) : (
                <p>
                  Moving {d.off_peak_shift.sessions_moved} home {d.off_peak_shift.sessions_moved === 1 ? "charge" : "charges"} to off-peak hours could have saved {formatMoney(d.off_peak_shift.could_have_saved, cur)}
                  {EST}
                </p>
              )}
            </Row>
            <p className="text-[11px] text-neutral-500">Worked out from your own charges and drives. Advice only: Rivolt never changes charging on the truck.</p>
          </>
        )}
      </div>
    </Card>
  );
}
