// The "I agree" gate: show it when the signed-in user's accepted version is not the current one.
import type { AuthUser } from "./api";

// Pages that must stay reachable without accepting: the legal pages themselves, sign-in and the
// OIDC hand-off. (The demo host has no signed-in user, so it never gates.)
const OPEN = ["/login", "/privacy", "/terms", "/auth/hydra/login", "/signup"];

export function termsGateActive(me: Pick<AuthUser, "terms"> | null | undefined, pathname: string): boolean {
  if (!me?.terms) return false;
  if (me.terms.accepted === me.terms.current) return false;
  if (pathname.startsWith("/demo")) return false;
  return !OPEN.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}
