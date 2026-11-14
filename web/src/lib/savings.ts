// "What moving home charging into your off-peak hours would have saved": the savings endpoint's answer and how the card words it.
// Always an estimate, always "could have saved", never "overpaid". Sessions that can't move are left out and the card says so.
import { api } from "./api";

export type SavingsResponse = {
  month?: string;
  tz?: string;
  currency?: string;
  could_have_saved?: number | null;
  sessions_moved?: number;
  sessions_left_out?: number;
  // Why sessions could not be moved, as counts by reason.
  left_out_reasons?: Record<string, number>;
  // How many home charges the estimate considered.
  sessions_considered?: number;
  as_charged_total?: number | null;
  if_moved_total?: number | null;
  // Set instead of a number when something is missing: "home_rate", "offpeak_schedule" or "more_home_sessions".
  needs?: string | null;
  // A sentence on how the figure was made, to show under it as text.
  basis?: string;
  // Share of home charging by energy that ran in off-peak hours (0 to 100); absent without a schedule, a home rate or enough sessions.
  offpeak_share_pct?: number | null;
  offpeak_share_sessions?: number;
  // A user on a flat rate gets only these (no estimate field of any kind): the reason and a sentence.
  flat_rate?: boolean;
  reason?: string;
};

export const fetchSavings = (month: string, signal?: AbortSignal) =>
  api.get<SavingsResponse>(`/api/ledger/savings?month=${encodeURIComponent(month)}`, signal);

export type SavingsView =
  | { kind: "flat"; basis: string }
  | { kind: "needs"; need: "schedule" | "home_rate" | "sessions" }
  | { kind: "nothing"; leftOut: number; reasons: string[]; considered: number | null }
  | { kind: "saved"; saved: number; moved: number; leftOut: number; asCharged: number | null; ifMoved: number | null; reasons: string[]; considered: number | null; basis: string };

const REASONS: Record<string, string> = {
  plugged_in_too_briefly: "plugged in too briefly for the charge to fit in one stretch",
  no_cheaper_hours: "already at the cheapest hours it could use",
};
// A reason key from the server as plain words; an unknown key is spelled out ("some_reason" -> "some reason").
export const reasonText = (key: string): string => REASONS[key] ?? key.replace(/[_-]+/g, " ").trim().toLowerCase();

const reasonList = (r: Record<string, number> | undefined): string[] =>
  Object.entries(r ?? {})
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([k, n]) => `${reasonText(k)} (${n})`);

export function savingsView(d: SavingsResponse): SavingsView {
  if (d.reason === "flat_rate" || d.flat_rate === true) return { kind: "flat", basis: d.basis ?? "" };
  if (d.needs === "offpeak_schedule") return { kind: "needs", need: "schedule" };
  if (d.needs === "home_rate") return { kind: "needs", need: "home_rate" };
  if (d.needs) return { kind: "needs", need: "sessions" };
  const saved = d.could_have_saved;
  const leftOut = d.sessions_left_out ?? 0;
  const moved = d.sessions_moved ?? 0;
  if (saved == null) return { kind: "needs", need: "sessions" };
  if (saved <= 0 || moved === 0) return { kind: "nothing", leftOut, reasons: reasonList(d.left_out_reasons), considered: d.sessions_considered ?? null };
  return { kind: "saved", saved, moved, leftOut, asCharged: d.as_charged_total ?? null, ifMoved: d.if_moved_total ?? null, reasons: reasonList(d.left_out_reasons), considered: d.sessions_considered ?? null, basis: d.basis ?? "" };
}

// The off-peak share for the share card's optional line: a number only when the server gave one. A flat-rate answer, one with a
// need, or none at all has no share, so the line and its toggle are not offered.
export const offpeakShareOf = (d: SavingsResponse | undefined): number | null => (typeof d?.offpeak_share_pct === "number" ? d.offpeak_share_pct : null);

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

export const savedHeadline = (moved: number, money: string): string =>
  `Moving ${moved} home ${plural(moved, "charge", "charges")} into your off-peak windows could have saved about ${money} (estimate).`;

export const leftOutLine = (n: number): string => `${n} ${plural(n, "session", "sessions")} could not be moved and ${plural(n, "is", "are")} left out.`;

// The savings coach: three insights over the last few months, each a figure or a reason it has none. Figures built on a guessed price say "estimated".
export type CoachBucket = { sessions: number; kwh: number; cost: number; per_kwh?: number | null; estimated: boolean; estimated_cost_share: number };
export type CoachResponse = {
  version: number;
  tz?: string;
  currency?: string;
  months?: string[];
  headline_id?: string;
  order?: string[];
  off_peak_shift: { needs?: string; could_have_saved?: number | null; sessions_moved: number; sessions_considered: number; estimated: boolean };
  cost_per_100: { needs?: string; per_100_mi?: number | null; per_100_km?: number | null; spend: number; miles: number; unpriced_share: number; estimated: boolean };
  cost_per_kwh: { needs?: string; home: CoachBucket; public: CoachBucket; fast_charging: CoachBucket; fast_premium_pct?: number | null; estimated: boolean };
};

export const fetchCoach = (signal?: AbortSignal) => api.get<CoachResponse>("/api/ledger/coach", signal);

const COACH_NEEDS: Record<string, string> = {
  priced_charges: "Not enough priced charging yet to work out what a kWh costs you.",
  more_driving: "Drive a little more (about 100 miles in the period) and your cost per 100 will show here.",
  unpriced_charges: "Too much of your charging has no price yet, so a cost per 100 would be misleading. Set a home rate or confirm prices on your receipts.",
  flat_rate: "You are on a flat rate, so moving charging to other hours would not change the cost.",
  home_rate: "Set your home rate to see what moving charging would save.",
  offpeak_schedule: "Add your off-peak hours to see what moving charging would save.",
  more_home_sessions: "A few more home charges are needed before this can be estimated.",
};
export const coachNeedsText = (need: string): string => COACH_NEEDS[need] ?? "Not enough data yet.";
