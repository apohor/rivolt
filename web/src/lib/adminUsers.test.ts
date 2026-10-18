import { describe, expect, it } from "vitest";
import { connection, filterUsers, lastData, needsAttention, noDataAlert, purge } from "./adminUsers";
import type { AdminUserRow } from "./api";

const NOW = Date.parse("2026-10-18T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const H = 3_600_000;
const D = 24 * H;
const row = (o: Partial<AdminUserRow> = {}): AdminUserRow => ({
  id: "u1", username: "a@example.com", email: "a@example.com", role: "user", disabled: false, created_at: ago(10 * D),
  vehicle_count: 1, drive_count: 3, import_count: 0, rivian_connected: true, needs_reauth: false, ...o,
});

describe("connection", () => {
  it("separates connected, needs re-auth (with its time) and not connected", () => {
    expect(connection(row()).state).toBe("connected");
    expect(connection(row({ rivian_connected: false })).state).toBe("none");
    expect(connection(row({ needs_reauth: true, needs_reauth_at: ago(H) }))).toEqual({ state: "reauth", since: ago(H) });
  });
});

describe("lastData", () => {
  it("is unknown until the server sends it, none when empty, else the age", () => {
    expect(lastData(row(), NOW).kind).toBe("unknown");
    expect(lastData(row({ last_data_at: null }), NOW).kind).toBe("none");
    expect(lastData(row({ last_data_at: ago(3 * H) }), NOW)).toMatchObject({ kind: "at", ageMs: 3 * H });
  });
});

describe("noDataAlert (leased, no data 2h)", () => {
  it("fires for a leased, connected user with no data for over two hours, or none at all", () => {
    expect(noDataAlert(row({ leased: true, last_data_at: ago(2 * H + 60_000) }), NOW)).toBe(true);
    expect(noDataAlert(row({ leased: true, last_data_at: null }), NOW)).toBe(true);
  });
  it("stays quiet within two hours, when not leased, or when the field is not supplied", () => {
    expect(noDataAlert(row({ leased: true, last_data_at: ago(2 * H - 60_000) }), NOW)).toBe(false);
    expect(noDataAlert(row({ leased: false, last_data_at: null }), NOW)).toBe(false);
    expect(noDataAlert(row({ leased: true }), NOW)).toBe(false);
    expect(noDataAlert(row({ last_data_at: null }), NOW)).toBe(false);
  });
  it("does not double up with re-auth or a disabled account", () => {
    expect(noDataAlert(row({ leased: true, last_data_at: null, needs_reauth: true }), NOW)).toBe(false);
    expect(noDataAlert(row({ leased: true, last_data_at: null, disabled: true }), NOW)).toBe(false);
    expect(noDataAlert(row({ leased: true, last_data_at: null, rivian_connected: false }), NOW)).toBe(false);
  });
});

describe("purge countdown", () => {
  const idle = (o: Partial<AdminUserRow>) => row({ vehicle_count: 0, rivian_connected: false, ...o });
  it("is quiet before the warning window", () => {
    expect(purge(idle({ created_at: ago(60 * D) }), NOW).state).toBe("none");
  });
  it("counts down to day 120 once past day 90", () => {
    expect(purge(idle({ created_at: ago(100 * D) }), NOW)).toEqual({ state: "pending", daysLeft: 20, warned: false });
    expect(purge(idle({ created_at: ago(119.5 * D) }), NOW)).toMatchObject({ daysLeft: 1 });
  });
  it("shows a sent warning even early, and never goes below 0", () => {
    expect(purge(idle({ created_at: ago(95 * D), purge_warned_at: ago(D) }), NOW)).toMatchObject({ warned: true, daysLeft: 25 });
    expect(purge(idle({ created_at: ago(130 * D) }), NOW)).toMatchObject({ daysLeft: 0 });
  });
  it("ignores connected users, admins and disabled accounts", () => {
    const old = ago(100 * D);
    expect(purge(row({ created_at: old }), NOW).state).toBe("none");
    expect(purge(idle({ created_at: old, role: "admin" }), NOW).state).toBe("none");
    expect(purge(idle({ created_at: old, disabled: true }), NOW).state).toBe("none");
  });
});

describe("filterUsers", () => {
  const rows = [
    row({ id: "1", username: "ann@example.com", display_name: "Ann" }),
    row({ id: "2", username: "bob@example.com", needs_reauth: true }),
    row({ id: "3", username: "cy@example.com", role: "admin" }),
    row({ id: "4", username: "di@example.com", disabled: true }),
    row({ id: "5", username: "ed@example.com", leased: true, last_data_at: ago(5 * H) }),
  ];
  const ids = (r: AdminUserRow[]) => r.map((x) => x.id);
  it("searches name, username and email", () => {
    expect(ids(filterUsers(rows, "ANN", "all", NOW))).toEqual(["1"]);
    expect(ids(filterUsers(rows, "example.com", "all", NOW))).toHaveLength(5);
  });
  it("filters by chip", () => {
    expect(ids(filterUsers(rows, "", "reauth", NOW))).toEqual(["2"]);
    expect(ids(filterUsers(rows, "", "nodata", NOW))).toEqual(["5"]);
    expect(ids(filterUsers(rows, "", "attention", NOW))).toEqual(["2", "5"]);
    expect(ids(filterUsers(rows, "", "admins", NOW))).toEqual(["3"]);
    expect(ids(filterUsers(rows, "", "disabled", NOW))).toEqual(["4"]);
    expect(needsAttention(rows[0], NOW)).toBe(false);
  });
});
