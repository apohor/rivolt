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
  reasons?: Record<string, number>;
  as_charged_total?: number | null;
  if_moved_total?: number | null;
  // Set instead of a number when something is missing.
  needs?: string | null;
  basis?: string;
};

export const fetchSavings = (month: string, signal?: AbortSignal) =>
  api.get<SavingsResponse>(`/api/ledger/savings?month=${encodeURIComponent(month)}`, signal);

export type SavingsView =
  | { kind: "needs"; need: "schedule" | "home_rate" | "sessions" }
  | { kind: "nothing"; leftOut: number; reasons: string[] }
  | { kind: "saved"; saved: number; moved: number; leftOut: number; asCharged: number | null; ifMoved: number | null; reasons: string[] };

// A reason key from the server as plain words ("no_window_fits" -> "no window fits").
export const reasonText = (key: string): string => key.replace(/[_-]+/g, " ").trim().toLowerCase();

const reasonList = (r: Record<string, number> | undefined): string[] =>
  Object.entries(r ?? {})
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([k, n]) => `${n} ${reasonText(k)}`);

export function savingsView(d: SavingsResponse): SavingsView {
  if (d.needs === "offpeak_schedule") return { kind: "needs", need: "schedule" };
  if (d.needs === "home_rate") return { kind: "needs", need: "home_rate" };
  if (d.needs) return { kind: "needs", need: "sessions" };
  const saved = d.could_have_saved;
  const leftOut = d.sessions_left_out ?? 0;
  const moved = d.sessions_moved ?? 0;
  if (saved == null) return { kind: "needs", need: "sessions" };
  if (saved <= 0 || moved === 0) return { kind: "nothing", leftOut, reasons: reasonList(d.reasons) };
  return { kind: "saved", saved, moved, leftOut, asCharged: d.as_charged_total ?? null, ifMoved: d.if_moved_total ?? null, reasons: reasonList(d.reasons) };
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

export const savedHeadline = (moved: number, money: string): string =>
  `Moving ${moved} home ${plural(moved, "charge", "charges")} into your off-peak windows could have saved about ${money} (estimate).`;

export const leftOutLine = (n: number): string => `${n} ${plural(n, "session", "sessions")} could not be moved and ${plural(n, "is", "are")} left out.`;
