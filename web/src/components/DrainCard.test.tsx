import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import DrainCard from "./DrainCard";
import { stubFetchRoutes } from "../test/fetchRoutes";

const win = (o: object = {}) => ({
  drain_pct_per_day: 0.9, drain_kwh_per_day: 1.2, idle_awake_hours_per_day: 1.3, sleeps: 22, quick_rewakes: 3,
  median_sleep_seconds: 18000, parked_hours: 120, power_state_observed_hours: 150, days_in_window: 7,
  days_with_data: 6, days_excluded: { insufficient: 1 }, sufficient: true, ...o,
});
const snap = (w7: object, w30: object = win({ drain_pct_per_day: 1.1 })) => ({
  version: 1, generated_at: "2026-10-14T00:00:00Z", tz: "UTC", pack_kwh: 135,
  windows: { "7d": w7, "30d": w30 },
  daily: Array.from({ length: 30 }, (_, i) => ({
    day: `2026-09-${String((i % 28) + 1).padStart(2, "0")}`, drain_pct: 1, idle_awake_hours: 1, parked_hours: 20,
    sleeps: 3, quick_rewakes: 0, excluded: null,
  })),
});

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DrainCard vehicleID="v1" />
    </QueryClientProvider>,
  );
}

describe("DrainCard", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("headlines %/day, counts sleeps, and claims no cause", async () => {
    stubFetchRoutes({ "/api/vehicles/v1/drain": { body: snap(win()) } });
    const { container } = renderCard();
    expect(await screen.findByText("0.9")).toBeInTheDocument();
    // Awake time lives in the Sleep card only (one source, no contradiction).
    expect(container.textContent).not.toMatch(/Awake \d/);
    expect(screen.getByText(/in the Sleep card below/)).toBeInTheDocument();
    expect(screen.getByText(/3 under a minute/)).toBeInTheDocument();
    expect(screen.getByText(/shows what happened, not why/)).toBeInTheDocument();
    const text = container.textContent ?? "";
    expect(text).not.toMatch(/sleep-safe|experiment|control|treatment|because|caused/i);
  });

  it("says so plainly and shows no rate when data is insufficient", async () => {
    stubFetchRoutes({
      "/api/vehicles/v1/drain": { body: snap(win({ sufficient: false, drain_pct_per_day: null, drain_kwh_per_day: null, parked_hours: 9 })) },
    });
    renderCard();
    expect(await screen.findByText(/Not enough parked time yet/)).toBeInTheDocument();
    expect(screen.getByText(/9\.0 h of parked data so far/)).toBeInTheDocument();
    expect(screen.queryByText("% / day")).toBeNull();
  });

  it("renders nothing when the endpoint is absent", async () => {
    stubFetchRoutes({}); // 404
    const { container } = renderCard();
    await vi.waitFor(() => expect(container.querySelector('[aria-label="Parked drain"]')).toBeNull());
    expect(container.textContent).toBe("");
  });
});
