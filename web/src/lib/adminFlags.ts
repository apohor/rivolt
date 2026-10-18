// Plain rules for the audience flags (money tab, statement email) and the AI call cap, kept apart
// from the screen so they can be tested.
import type { AudienceFlag } from "./api";

export type Audience = "nobody" | "selected" | "everyone";

export function audienceOf(f: AudienceFlag | undefined): Audience {
  if (f?.all) return "everyone";
  return (f?.users?.length ?? 0) > 0 ? "selected" : "nobody";
}

export function audienceLabel(f: AudienceFlag | undefined): string {
  const a = audienceOf(f);
  if (a === "everyone") return "Everyone";
  if (a === "selected") return `${f!.users!.length} user${f!.users!.length === 1 ? "" : "s"}`;
  return "Nobody";
}

// What to send for a chosen audience: everyone drops the list (the server ignores it then), nobody
// clears it, and a selection keeps the chosen ids.
export function audienceBody(a: Audience, users: string[]): { all: boolean; users: string[] } {
  if (a === "everyone") return { all: true, users: [] };
  if (a === "selected") return { all: false, users };
  return { all: false, users: [] };
}

// The sentence asked before saving, in the user's terms.
export function audienceConfirm(feature: string, from: Audience, to: Audience, n: number): string {
  if (to === "everyone") return `Turn ${feature} on for everyone?`;
  if (to === "nobody") return `Turn ${feature} off for everyone?`;
  return from === "everyone" ? `Limit ${feature} to ${n} user${n === 1 ? "" : "s"} (it is on for everyone now)?` : `Set ${feature} for ${n} user${n === 1 ? "" : "s"}?`;
}

export function capLabel(limit: number | undefined): string {
  return limit && limit > 0 ? `${limit.toLocaleString()} calls per user per day` : "No cap";
}

// The cap input: a whole number, 0 or more (0 removes the cap).
export function parseCap(raw: string): number | null {
  if (raw.trim() === "") return null;
  const n = Number(raw);
  return Number.isInteger(n) && n >= 0 ? n : null;
}
