// Client-side state for the admin "View as user" feature.
// internal/api/auth_mw.go is the actual enforcement point — this
// module only carries UI state and the header value the fetch
// wrapper in lib/api.ts attaches to every request.
//
// The active target lives in sessionStorage (not localStorage) so it
// clears when the tab closes and never leaks across a shared-browser
// install between logins — same reasoning as SignOutButton's full
// page reload on sign-out.
import { useEffect, useState } from "react";

// IMPERSONATE_HEADER must match internal/api.ImpersonateHeader.
export const IMPERSONATE_HEADER = "X-Rivolt-Impersonate";

const STORAGE_KEY = "rivolt.impersonate";
const CHANGE_EVENT = "rivolt:impersonation-change";

export type ImpersonationTarget = { id: string; email: string };

function read(): ImpersonationTarget | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<ImpersonationTarget>;
    if (!parsed?.id) return null;
    return { id: parsed.id, email: parsed.email ?? "" };
  } catch {
    return null;
  }
}

// getImpersonationTarget returns the active impersonation target, or
// null. Synchronous, non-React accessor — used by the api.ts fetch
// wrapper on every request.
export function getImpersonationTarget(): ImpersonationTarget | null {
  return read();
}

// impersonationHeaderID returns the value the fetch wrapper should
// send as X-Rivolt-Impersonate, or null when no impersonation is
// active.
export function impersonationHeaderID(): string | null {
  return read()?.id ?? null;
}

// startImpersonation begins viewing the app as `target`. Callers
// should follow up with a full page reload (not client-side
// navigation) so every in-memory cache (React Query, component
// state) is rebuilt from scratch under the new identity instead of
// mixing admin-fetched and target-fetched data in the same cache —
// the same tenant-isolation reasoning CLAUDE.md applies to the
// server side applies to the browser's own caches.
export function startImpersonation(target: ImpersonationTarget): void {
  sessionStorage.setItem(STORAGE_KEY, JSON.stringify(target));
  window.dispatchEvent(new Event(CHANGE_EVENT));
}

// stopImpersonation clears the active target. Callers should also
// force a full page reload for the same cache-isolation reason as
// startImpersonation.
export function stopImpersonation(): void {
  sessionStorage.removeItem(STORAGE_KEY);
  window.dispatchEvent(new Event(CHANGE_EVENT));
}

// useImpersonationTarget is the React-friendly accessor. Re-renders
// on start/stop — including changes made from a different component
// via the custom event — so the banner and any gated UI (e.g. the
// Live nav link) stay in sync without prop drilling.
export function useImpersonationTarget(): ImpersonationTarget | null {
  const [target, setTarget] = useState<ImpersonationTarget | null>(() => read());
  useEffect(() => {
    const onChange = () => setTarget(read());
    window.addEventListener(CHANGE_EVENT, onChange);
    return () => window.removeEventListener(CHANGE_EVENT, onChange);
  }, []);
  return target;
}
