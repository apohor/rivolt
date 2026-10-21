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
};

export const fetchSavings = (month: string, signal?: AbortSignal) =>
  api.get<SavingsResponse>(`/api/ledger/savings?month=${encodeURIComponent(month)}`, signal);

export type SavingsView =
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

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

export const savedHeadline = (moved: number, money: string): string =>
  `Moving ${moved} home ${plural(moved, "charge", "charges")} into your off-peak windows could have saved about ${money} (estimate).`;

export const leftOutLine = (n: number): string => `${n} ${plural(n, "session", "sessions")} could not be moved and ${plural(n, "is", "are")} left out.`;
