import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { DEVICE_WEATHER_KEY, usePlannerPrefs } from "./plannerPrefs";

const server = { drive_mode: "SPORT", corridor: "normal", start_exclude_mi: 0, min_fast_kw: 0, layers_on: ["dcfc"], preferred_only: false, weather_on: true, radar_on: false };

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

function stub() {
  const puts: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
    let body: unknown = server;
    if (init?.method === "PUT") {
      const sent = JSON.parse(String(init.body));
      puts.push(sent);
      body = { ...server, ...sent };
    }
    return { ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify(body), json: async () => body } as unknown as Response;
  }));
  return puts;
}

beforeEach(() => localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe("usePlannerPrefs", () => {
  it("copies a weather choice made on this device into the account setting once, and shows it at once", async () => {
    localStorage.setItem(DEVICE_WEATHER_KEY, "off");
    const puts = stub();
    const { result } = renderHook(() => usePlannerPrefs(), { wrapper });
    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.prefs.weather_on).toBe(false); // not the server's "on" for even one render
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toMatchObject({ weather_on: false, drive_mode: "SPORT" }); // the whole settings go with it
    await waitFor(() => expect(localStorage.getItem(DEVICE_WEATHER_KEY)).toBeNull());
    expect(result.current.prefs.weather_on).toBe(false);
  });

  it("drops the device copy without a write when it already agrees with the account", async () => {
    localStorage.setItem(DEVICE_WEATHER_KEY, "on");
    const puts = stub();
    const { result } = renderHook(() => usePlannerPrefs(), { wrapper });
    await waitFor(() => expect(result.current.settled).toBe(true));
    await waitFor(() => expect(localStorage.getItem(DEVICE_WEATHER_KEY)).toBeNull());
    expect(puts).toHaveLength(0);
  });

  it("changes nothing for a device that never chose", async () => {
    const puts = stub();
    const { result } = renderHook(() => usePlannerPrefs(), { wrapper });
    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.prefs.weather_on).toBe(true);
    expect(puts).toHaveLength(0);
  });
});
