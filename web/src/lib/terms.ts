// The "I agree" gate: shown only to accounts the server says must accept (terms.required, true only
// for new accounts that have not accepted the current version). Existing users never see it.
import type { AuthUser } from "./api";

// Pages that must stay reachable without accepting: the legal pages themselves, sign-in and the
// OIDC hand-off. (The demo host has no signed-in user, so it never gates.)
const OPEN = ["/login", "/privacy", "/terms", "/moving-from-electrafi", "/safe-to-connect", "/auth/hydra/login", "/signup"];

export function termsGateActive(me: Pick<AuthUser, "terms"> | null | undefined, pathname: string): boolean {
  if (me?.terms?.required !== true) return false;
  if (pathname.startsWith("/demo")) return false;
  return !OPEN.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}
