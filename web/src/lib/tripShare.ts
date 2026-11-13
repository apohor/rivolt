import { ApiError, type SharedTrip } from "./api";

// Shared trip links. The address is /s#<token>: the token is in the fragment so it never reaches a server log, a Referer or a tile host. The
// public page takes it out of the address bar the moment it reads it (so nothing that reports the address later, an error report or a
// screenshot of the bar, can carry it) and sends it in a POST body.
const TOKEN_RE = /^[A-Za-z0-9_-]{22}$/;

let taken: { token: string | null } | null = null;

// readShareToken returns the token in location.hash and clears the hash from the address (history.replaceState: no new history entry). The
// token is kept in memory for this page load, so a second call (React's double render, a retry) still has it. A wrong-looking token gives null.
export function readShareToken(): string | null {
  if (typeof window === "undefined") return null;
  const raw = window.location.hash.replace(/^#/, "");
  if (raw) {
    taken = { token: TOKEN_RE.test(raw) ? raw : null };
    window.history.replaceState(window.history.state, "", window.location.pathname + window.location.search);
  }
  return taken?.token ?? null;
}

export type SharedTripResult =
  | { kind: "ok"; trip: SharedTrip }
  | { kind: "notfound" }
  | { kind: "limited"; retryAfter: number | null }
  | { kind: "busy"; retryAfter: number | null }
  | { kind: "error" };

// fetchSharedTrip asks for the trip with no cookies and no session (credentials omitted); the answer is never cached.
export async function fetchSharedTrip(token: string, signal?: AbortSignal): Promise<SharedTripResult> {
  let res: Response;
  try {
    res = await fetch("/api/public/trips/share", {
      method: "POST",
      credentials: "omit",
      cache: "no-store",
      referrerPolicy: "no-referrer",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token }),
      signal,
    });
  } catch {
    return { kind: "error" };
  }
  const retry = Number.parseInt(res.headers.get("Retry-After") ?? "", 10);
  const retryAfter = Number.isFinite(retry) && retry > 0 ? retry : null;
  if (res.status === 404 || res.status === 400) return { kind: "notfound" };
  if (res.status === 429) return { kind: "limited", retryAfter };
  if (res.status === 503) return { kind: "busy", retryAfter };
  if (!res.ok) return { kind: "error" };
  try {
    const trip = (await res.json()) as SharedTrip;
    if (!trip || !Array.isArray(trip.stops) || !trip.route || !Array.isArray(trip.route.segments) || !trip.totals) return { kind: "error" };
    return { kind: "ok", trip };
  } catch {
    return { kind: "error" };
  }
}

export const SHARE_EXPIRY: { days: 1 | 7 | 30 | null; label: string }[] = [
  { days: 1, label: "1 day" },
  { days: 7, label: "7 days" },
  { days: 30, label: "30 days" },
  { days: null, label: "Until I turn it off" },
];

export const SHARE_TITLE_MAX = 80;

// shareCreateMessage is what a failed create says, by the server's code. Never the raw error.
export function shareCreateMessage(e: unknown): string {
  if (e instanceof ApiError) {
    const code = e.body && typeof e.body === "object" ? (e.body as { code?: unknown }).code : undefined;
    if (e.status === 409 && code === "unsupported") return "This trip can't be shared: multi-day trips and trips with no route line are not supported yet.";
    if (e.status === 409 && code === "too_many_links") return "You have 25 live links. Turn one off below to make another.";
    if (e.status === 422 && code === "too_short") return "This trip is too short to share: the start and end are hidden, and little would be left.";
    if (e.status === 429) return "Too many links in a short time. Try again in a few minutes.";
    if (e.status === 404) return "This trip is no longer saved.";
  }
  return "Couldn't create the link. Try again.";
}

// shareLinkState: what to call a listed link.
export function shareLinkState(l: { expires_at: string | null; revoked_at: string | null }, now = Date.now()): "live" | "revoked" | "expired" {
  if (l.revoked_at) return "revoked";
  if (l.expires_at && new Date(l.expires_at).getTime() <= now) return "expired";
  return "live";
}

// stopTime formats a time in the trip's own zone (the snapshot's tz), else the browser's.
export function tripTime(iso: string | undefined, tz: string | undefined, opts: Intl.DateTimeFormatOptions): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  try {
    return d.toLocaleString(undefined, { ...opts, ...(tz ? { timeZone: tz } : {}) });
  } catch {
    return d.toLocaleString(undefined, opts);
  }
}
