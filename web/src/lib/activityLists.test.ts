import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { REFRESH_AFTER_CLOSE_MS, sessionInFlight, useRefreshListsWhenSessionCloses } from "./activityLists";

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

describe("the re-reads after a session closes", () => {
  afterEach(() => vi.useRealTimers());
  const setup = () => {
    vi.useFakeTimers();
    const qc = new QueryClient();
    qc.setQueryData(["drives", "all"], []);
    const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client: qc }, children);
    const hook = renderHook(() => useRefreshListsWhenSessionCloses(), { wrapper });
    const spy = vi.spyOn(qc, "invalidateQueries");
    return { qc, spy, hook };
  };
  const close = (qc: QueryClient) => {
    qc.setQueryData(["rivian", "state", "v1"], st({ gear: "D" }));
    qc.setQueryData(["rivian", "state", "v1"], st({}));
  };

  it("reads the lists again at each delay, because the drive is saved a little after the truck stops", () => {
    const { qc, spy } = setup();
    close(qc);
    const afterClose = spy.mock.calls.filter((c) => (c[0] as { queryKey: string[] }).queryKey[0] === "drives").length;
    expect(afterClose).toBe(1); // at once
    for (const [i, ms] of REFRESH_AFTER_CLOSE_MS.entries()) {
      vi.advanceTimersByTime(ms - (i === 0 ? 0 : REFRESH_AFTER_CLOSE_MS[i - 1]));
      expect(spy.mock.calls.filter((c) => (c[0] as { queryKey: string[] }).queryKey[0] === "drives").length).toBe(afterClose + i + 1);
    }
    vi.advanceTimersByTime(10 * 60_000);
    expect(spy.mock.calls.filter((c) => (c[0] as { queryKey: string[] }).queryKey[0] === "drives").length).toBe(afterClose + REFRESH_AFTER_CLOSE_MS.length);
  });

  it("stops when a new session starts, and when the page goes away", () => {
    const a = setup();
    close(a.qc);
    a.qc.setQueryData(["rivian", "state", "v1"], st({ gear: "D" }));
    const n = a.spy.mock.calls.length;
    vi.advanceTimersByTime(10 * 60_000);
    expect(a.spy.mock.calls.length).toBe(n);
    vi.useRealTimers();
    const b = setup();
    close(b.qc);
    const m = b.spy.mock.calls.length;
    b.hook.unmount();
    vi.advanceTimersByTime(10 * 60_000);
    expect(b.spy.mock.calls.length).toBe(m);
  });
});
