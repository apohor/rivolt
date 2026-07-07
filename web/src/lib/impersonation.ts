// Admin "View as user" client state.
//
// When an admin starts impersonating, the target's id + email are held
// in sessionStorage (tab-scoped, cleared when the tab closes — a
// forgotten impersonation can't outlive the session). Every API call
// then carries the X-Rivolt-Impersonate header (see lib/api.ts), which
// the server honours only for admins and only for GETs. The whole app
// re-renders as the target because start/stop force a full navigation
// so TanStack Query refetches everything under the new identity.
//
// This is deliberately not React state: the header injection lives in
// the plain fetch wrapper, which has no hook access, so sessionStorage
// is the single source of truth both it and the UI read.

export const IMPERSONATE_HEADER = "X-Rivolt-Impersonate";

const KEY = "rivolt.impersonate";

export type Impersonation = { uid: string; email: string };

// current returns the active impersonation, or null. Tolerates a
// malformed/legacy value by treating it as "not impersonating".
export function current(): Impersonation | null {
  let raw: string | null;
  try {
    raw = sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as Partial<Impersonation>;
    if (v && typeof v.uid === "string" && v.uid) {
      return { uid: v.uid, email: typeof v.email === "string" ? v.email : "" };
    }
  } catch {
    /* fall through */
  }
  return null;
}

// targetHeader returns the header value to attach, or null when not
// impersonating. Used by the API fetch wrapper.
export function targetHeader(): string | null {
  return current()?.uid ?? null;
}

// start begins impersonating a user and reloads to "/" so every query
// refetches as the target. Callers should already have confirmed the
// target is a non-admin (the server enforces this regardless).
export function start(uid: string, email: string): void {
  try {
    sessionStorage.setItem(KEY, JSON.stringify({ uid, email }));
  } catch {
    /* sessionStorage unavailable — nothing to do */
  }
  window.location.assign("/");
}

// stop ends impersonation and returns to the admin page as the real
// admin. A full navigation guarantees no target-scoped query lingers.
export function stop(): void {
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    /* ignore */
  }
  window.location.assign("/admin");
}
