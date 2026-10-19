import { describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { sessionInFlight, useRefreshListsWhenSessionCloses } from "./activityLists";

const st = (o: object) => ({ speed_kph: 0, gear: "P", charger_power_kw: 0, charger_state: "", ...o }) as never;

describe("sessionInFlight", () => {
  it("is true while driving or charging and false when parked", () => {
    expect(sessionInFlight(st({ gear: "D" }))).toBe(true);
    expect(sessionInFlight(st({ speed_kph: 40 }))).toBe(true);
    expect(sessionInFlight(st({ charger_power_kw: 11 }))).toBe(true);
    expect(sessionInFlight(st({ charger_state: "charging_active" }))).toBe(true);
    expect(sessionInFlight(st({}))).toBe(false);
  });
});

describe("useRefreshListsWhenSessionCloses", () => {
  it("invalidates the lists once when a session ends, and not while it runs or when idle", () => {
    const qc = new QueryClient();
    qc.setQueryData(["drives", "all"], []);
    qc.setQueryData(["charges", "all"], []);
    const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client: qc }, children);
    renderHook(() => useRefreshListsWhenSessionCloses(), { wrapper });
    const stale = () => qc.getQueryState(["drives", "all"])!.isInvalidated;
    qc.setQueryData(["rivian", "state", "v1"], st({}));
    expect(stale()).toBe(false);
    qc.setQueryData(["rivian", "state", "v1"], st({ gear: "D" }));
    expect(stale()).toBe(false);
    qc.setQueryData(["rivian", "state", "v1"], st({ gear: "D", speed_kph: 50 }));
    expect(stale()).toBe(false);
    qc.setQueryData(["rivian", "state", "v1"], st({}));
    expect(stale()).toBe(true);
    expect(qc.getQueryState(["charges", "all"])!.isInvalidated).toBe(true);
  });
});
