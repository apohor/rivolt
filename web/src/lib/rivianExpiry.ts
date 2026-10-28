// The Rivian sign-in ends 180 days after it and nothing extends it; signing in again is the renewal. The server sends when
// (session_expires_at) and the whole days left (session_days_left, 0 when past) while signed in and not needs_reauth.
// The wording is the owner-approved N49's: the banner from 7 days before, and "Connected · expires Nov 3" in Settings.

export const EXPIRY_BANNER_DAYS = 7;

// "Nov 3", in the reader's own language and time zone; "" when the date is missing or unreadable.
export function expiryDate(iso: string | undefined | null, locale?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleDateString(locale, { month: "short", day: "numeric" });
}

// The banner line, or null when the sign-in is not within a week of ending. With no readable date it falls back to the days left.
export function expiryNotice(daysLeft: number | undefined, expiresAt: string | undefined | null, locale?: string): string | null {
  if (typeof daysLeft !== "number" || !Number.isFinite(daysLeft) || daysLeft > EXPIRY_BANNER_DAYS) return null;
  const date = expiryDate(expiresAt, locale);
  if (date) return `Your Rivian connection expires ${date}.`;
  return daysLeft <= 0 ? "Your Rivian connection expires today." : daysLeft === 1 ? "Your Rivian connection expires in 1 day." : `Your Rivian connection expires in ${Math.floor(daysLeft)} days.`;
}
