import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { DEVICE_WEATHER_KEY, plannerWithDefaults, usePlannerPrefs, useSavePlannerPrefs } from "./plannerPrefs";

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

describe("plannerWithDefaults", () => {
  it("starts with every charger layer off for a user who never chose", () => {
    expect(plannerWithDefaults(undefined).layers_on).toEqual([]);
  });
});

describe("useSavePlannerPrefs", () => {
  it("sends two quick saves one after the other, so the latest choice is the one kept", async () => {
    // The server answers each PUT only when the test says so, and the first answer is held back.
    const sent: Record<string, unknown>[] = [];
    const answers: (() => void)[] = [];
    vi.stubGlobal("fetch", vi.fn((_url: RequestInfo | URL, init?: RequestInit) => {
      const body = init?.method === "PUT" ? JSON.parse(String(init.body)) : server;
      if (init?.method === "PUT") sent.push(body);
      const res = { ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify(body), json: async () => body } as unknown as Response;
      return init?.method === "PUT" ? new Promise<Response>((done) => answers.push(() => done(res))) : Promise.resolve(res);
    }));
    const { result } = renderHook(() => ({ read: usePlannerPrefs(), save: useSavePlannerPrefs() }), { wrapper });
    await waitFor(() => expect(result.current.read.loaded).toBe(true));
    result.current.save.mutate({ layers_on: ["dcfc", "hotels"] });
    result.current.save.mutate({ layers_on: ["hotels"] });
    await waitFor(() => expect(sent).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(sent).toHaveLength(1); // the second waits for the first
    answers[0]();
    await waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1].layers_on).toEqual(["hotels"]);
    answers[1]();
    await waitFor(() => expect(result.current.save.isPending).toBe(false));
    expect(result.current.read.prefs.layers_on).toEqual(["hotels"]);
  });
});

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
