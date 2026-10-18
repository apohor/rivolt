// What the admin Users table says about each user, derived from the list row. Pure, so the rules
// (the red "leased, no data" flag, the purge countdown) are tested apart from the table.
import type { AdminUserRow } from "./api";

const HOUR = 3_600_000;
const DAY = 24 * HOUR;
// db.InactiveWarnAfter / InactiveDeleteAfter: an account that never connected Rivian is warned at
// day 90 and deleted at day 120 after signup (admins and disabled accounts are never touched).
const PURGE_WARN_DAYS = 90;
const PURGE_DELETE_DAYS = 120;
export const NO_DATA_AFTER_MS = 2 * HOUR;

export type Connection = { state: "connected" | "reauth" | "none"; since?: string | null };

export function connection(u: AdminUserRow): Connection {
  if (u.needs_reauth) return { state: "reauth", since: u.needs_reauth_at ?? null };
  return { state: u.rivian_connected ? "connected" : "none" };
}

export type LastData = { kind: "unknown" } | { kind: "none" } | { kind: "at"; at: string; ageMs: number };

export function lastData(u: AdminUserRow, now: number): LastData {
  if (u.last_data_at === undefined) return { kind: "unknown" };
  if (u.last_data_at === null) return { kind: "none" };
  const t = Date.parse(u.last_data_at);
  if (!Number.isFinite(t)) return { kind: "none" };
  return { kind: "at", at: u.last_data_at, ageMs: Math.max(0, now - t) };
}

// "Leased, no data 2h": the server should be recording this truck (it is leased to a pod, Rivian is
// connected and not asking for a re-sign-in) but nothing has arrived for two hours.
export function noDataAlert(u: AdminUserRow, now: number): boolean {
  if (u.leased !== true || !u.rivian_connected || u.needs_reauth || u.disabled) return false;
  const d = lastData(u, now);
  return d.kind === "none" || (d.kind === "at" && d.ageMs > NO_DATA_AFTER_MS);
}

export type Purge = { state: "none" } | { state: "pending"; daysLeft: number; warned: boolean };

// Inactive-purge countdown. Shown once the account is in the warning window (day 90 on) or a
// warning has been sent; an account that connected Rivian, an admin or a disabled one is out of scope.
export function purge(u: AdminUserRow, now: number): Purge {
  const inScope = u.role !== "admin" && !u.disabled && !u.rivian_connected && u.vehicle_count === 0;
  if (!inScope) return { state: "none" };
  const created = Date.parse(u.created_at);
  if (!Number.isFinite(created)) return { state: "none" };
  const warned = !!u.purge_warned_at;
  const age = (now - created) / DAY;
  if (!warned && age < PURGE_WARN_DAYS) return { state: "none" };
  const daysLeft = Math.max(0, Math.ceil(PURGE_DELETE_DAYS - age));
  return { state: "pending", daysLeft, warned };
}

export function needsAttention(u: AdminUserRow, now: number): boolean {
  return u.needs_reauth || noDataAlert(u, now) || purge(u, now).state === "pending";
}

export type UserFilter = "all" | "attention" | "reauth" | "nodata" | "admins" | "disabled";

export function filterUsers(rows: AdminUserRow[], q: string, f: UserFilter, now: number): AdminUserRow[] {
  const needle = q.trim().toLowerCase();
  return rows.filter((u) => {
    if (needle && ![u.username, u.email, u.display_name].some((v) => v?.toLowerCase().includes(needle))) return false;
    switch (f) {
      case "attention": return needsAttention(u, now);
      case "reauth": return u.needs_reauth;
      case "nodata": return noDataAlert(u, now);
      case "admins": return u.role === "admin";
      case "disabled": return u.disabled;
      default: return true;
    }
  });
}
