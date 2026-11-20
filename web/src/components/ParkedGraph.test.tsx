import { afterEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { ParkedSeries } from "../lib/api";
import { backend } from "../lib/api";
import { useParkedSeries } from "./ParkedGraph";

const pts = Array.from({ length: 5 }, (_, i) => ({ t: new Date(Date.UTC(2026, 10, 20, i)).toISOString(), soc: 70 - i, seg: 0 }));
const series = (since: string) => ({ version: 1, from: pts[0].t, to: pts[4].t, parked_since: since, parked: true, change_pct: -4, points: pts, sleeps: [], wakes: [], figures: null, projection: null }) as unknown as ParkedSeries;

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return renderHook(({ id, on }: { id: string; on: boolean }) => useParkedSeries(id, on), { wrapper, initialProps: { id: "a", on: true } });
}

afterEach(() => vi.restoreAllMocks());

describe("useParkedSeries: the chip waits for an answer asked for after it was enabled", () => {
  it("does not hand back the cached series of an earlier parking while the new one is out", async () => {
    let release: (s: ParkedSeries) => void = () => {};
    const spy = vi.spyOn(backend, "vehicleParkedSeries").mockResolvedValueOnce(series("2026-11-20T01:00:00Z")).mockImplementationOnce(() => new Promise((r) => (release = r)));
    const h = setup();
    expect(h.result.current).toEqual({ series: null, waiting: true });
    await waitFor(() => expect(h.result.current.series?.parked_since).toBe("2026-11-20T01:00:00Z"));
    expect(h.result.current.waiting).toBe(false);
    // The truck drives, so the graph is off, then it parks again: the old answer is cached, the new one is held.
    h.rerender({ id: "a", on: false });
    expect(h.result.current.series).toBeNull();
    h.rerender({ id: "a", on: true });
    await waitFor(() => expect(spy).toHaveBeenCalledTimes(2));
    expect(h.result.current).toEqual({ series: null, waiting: true });
    await act(async () => release(series("2026-11-20T09:00:00Z")));
    await waitFor(() => expect(h.result.current.series?.parked_since).toBe("2026-11-20T09:00:00Z"));
    expect(h.result.current.waiting).toBe(false);
  });

  it("asks again when a truck is switched back to within the app's 30 s fresh window", async () => {
    let n = 0;
    const spy = vi.spyOn(backend, "vehicleParkedSeries").mockImplementation(async () => series(`2026-11-20T0${++n}:00:00Z`));
    const h = setup();
    await waitFor(() => expect(h.result.current.series?.parked_since).toBe("2026-11-20T01:00:00Z"));
    h.rerender({ id: "b", on: true });
    await waitFor(() => expect(h.result.current.series?.parked_since).toBe("2026-11-20T02:00:00Z"));
    h.rerender({ id: "a", on: true });
    await waitFor(() => expect(h.result.current.series?.parked_since).toBe("2026-11-20T03:00:00Z"));
    expect(spy).toHaveBeenCalledTimes(3);
    expect(h.result.current.waiting).toBe(false);
  });

  it("starts a new wait for a second truck after the first one's 3 s have passed", async () => {
    vi.spyOn(backend, "vehicleParkedSeries").mockImplementation(() => new Promise(() => {}));
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const h = setup();
      await act(async () => void vi.advanceTimersByTime(3100));
      expect(h.result.current.waiting).toBe(false);
      h.rerender({ id: "b", on: true });
      expect(h.result.current.waiting).toBe(true);
      await act(async () => void vi.advanceTimersByTime(3100));
      expect(h.result.current.waiting).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops waiting when the read fails", async () => {
    vi.spyOn(backend, "vehicleParkedSeries").mockRejectedValue(new Error("down"));
    const h = setup();
    await waitFor(() => expect(h.result.current.waiting).toBe(false));
    expect(h.result.current.series).toBeNull();
  });
});
