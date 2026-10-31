// Where Stripe sends the buyer back: /settings?checkout=success#plan (paid) or ?checkout=cancelled#plan (backed out). The plan itself
// arrives by webhook within seconds, so the Plan card asks /api/auth/me again a few times and says what it knows meanwhile.

export type CheckoutReturn = "success" | "cancelled" | null;

export const POLL_EVERY_MS = 2000;
export const POLL_TIMES = 8;

export function checkoutReturn(search: string): CheckoutReturn {
  const v = new URLSearchParams(search).get("checkout");
  return v === "success" || v === "cancelled" ? v : null;
}

// The address without ?checkout=, hash kept.
export function withoutCheckout(href: string): string {
  const u = new URL(href);
  u.searchParams.delete("checkout");
  return u.pathname + (u.search || "") + u.hash;
}

export function checkoutMessage(r: Exclude<CheckoutReturn, null>, proIsOn: boolean, gaveUp: boolean): string {
  if (r === "cancelled") return "No charge was made.";
  if (proIsOn) return "Thanks, Pro is on.";
  return gaveUp
    ? "Thanks. Your payment went through, but your plan has not updated yet. Reload in a minute; if it still shows Free, contact support."
    : "Thanks. Pro turns on in a moment.";
}
