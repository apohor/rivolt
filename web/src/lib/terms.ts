// The "I agree" gate: shown only to accounts the server says must accept (terms.required, true only
// for new accounts that have not accepted the current version). Existing users never see it.
import { ApiError, type AuthUser } from "./api";

// Pages that must stay reachable without accepting: the legal pages themselves, sign-in and the
// OIDC hand-off. (The demo host has no signed-in user, so it never gates.)
const OPEN = ["/login", "/privacy", "/terms", "/changelog", "/moving-from-electrafi", "/safe-to-connect", "/support", "/lease-calculator", "/charging-cost", "/auth/hydra/login", "/signup"];

export function termsGateActive(me: Pick<AuthUser, "terms"> | null | undefined, pathname: string): boolean {
  if (me?.terms?.required !== true) return false;
  if (pathname.startsWith("/demo")) return false;
  return !OPEN.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

// The server answers 403 {"error":"terms_required"} to a data route until the current Terms are accepted. The "I agree" screen is
// driven by /api/auth/me, so a request that gets this answer means the page's copy of `me` is stale: refetch it and the gate shows.
export function isTermsRequiredError(e: unknown): boolean {
  return e instanceof ApiError && e.status === 403 && (e.body as { error?: string } | null)?.error === "terms_required";
}
