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
    // While the request is in flight the titled skeleton shows; once the 404
    // resolves it is gone and nothing is rendered.
    await vi.waitFor(() => expect(container.querySelector('[data-testid="drain-skeleton"]')).toBeNull());
    expect(container.querySelector('[aria-label="Parked drain"]')).toBeNull();
    expect(container.textContent).toBe("");
  });

  it("shows a titled skeleton while the request is pending", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    const { container } = renderCard();
    expect(container.querySelector('[data-testid="drain-skeleton"]')).not.toBeNull();
    expect(container.textContent).toContain("Parked drain");
  });

  const split = (o: object = {}) => ({
    sufficient: true, asleep_hours: 120, awake_hours: 14, asleep_points_lost: 1, awake_points_lost: 5, awake_share_of_loss: 5 / 6, awake_share_of_time: 0.1, min_hours: 48, min_points_lost: 3, ...o,
  });

  it("says when the loss happened, as an estimate, with a two-part bar and no cause", async () => {
    stubFetchRoutes({ "/api/vehicles/v1/drain": { body: snap(win({ split: split() })) } });
    const { container } = renderCard();
    expect(await screen.findByText(/Of the 6% your truck lost while parked this week, about 5% was while it was awake/)).toBeInTheDocument();
    expect(screen.getByText(/whole percents/)).toBeInTheDocument();
    expect(container.querySelector('[data-drain-split="ok"] [role="img"]')?.getAttribute("aria-label")).toMatch(/83% of the loss was while awake, 17% while asleep/);
    expect(container.textContent ?? "").not.toMatch(/because|caused|sleep-safe/i);
  });

  it("hides a part with zero points instead of drawing a sliver", async () => {
    stubFetchRoutes({ "/api/vehicles/v1/drain": { body: snap(win({ split: split({ asleep_points_lost: 0, awake_points_lost: 4, awake_share_of_loss: 1 }) })) } });
    const { container } = renderCard();
    await screen.findByText(/Of the 4% your truck lost/);
    expect(container.querySelectorAll('[data-drain-split="ok"] [role="img"] > div')).toHaveLength(1);
    expect(screen.queryByText("Asleep")).toBeNull();
  });

  it("says what it needs when there is too little, and nothing when the power state is unreliable or the split is absent", async () => {
    const off = { sufficient: false, asleep_hours: null, awake_hours: null, asleep_points_lost: null, awake_points_lost: null, awake_share_of_loss: null, awake_share_of_time: null };
    stubFetchRoutes({ "/api/vehicles/v1/drain": { body: snap(win({ split: split({ ...off, reason: "not_enough_data" }) })) } });
    renderCard();
    expect(await screen.findByText(/needs about two days of parked history with a few points lost/)).toBeInTheDocument();
    cleanup();
    stubFetchRoutes({ "/api/vehicles/v1/drain": { body: snap(win({ split: split({ ...off, reason: "power_state_gaps" }) })) } });
    renderCard();
    expect(await screen.findByText(/power state wasn't recorded often enough/)).toBeInTheDocument();
    cleanup();
    stubFetchRoutes({ "/api/vehicles/v1/drain": { body: snap(win({ split: split({ ...off, reason: "power_state_unreliable" }) })) } });
    const a = renderCard();
    await screen.findByText("0.9");
    expect(a.container.querySelector("[data-drain-split]")).toBeNull();
    cleanup();
    stubFetchRoutes({ "/api/vehicles/v1/drain": { body: snap(win()) } });
    const b = renderCard();
    await screen.findByText("0.9");
    expect(b.container.querySelector("[data-drain-split]")).toBeNull();
  });

  it("falls back to the other window's split when the selected one is too thin", async () => {
    const off = { sufficient: false, reason: "not_enough_data", asleep_hours: 10, awake_hours: 1, asleep_points_lost: null, awake_points_lost: null, awake_share_of_loss: null, awake_share_of_time: null, min_hours: 48, min_points_lost: 3 };
    stubFetchRoutes({ "/api/vehicles/v1/drain": { body: snap(win({ split: off }), win({ drain_pct_per_day: 1.1, split: split({ asleep_points_lost: 10, awake_points_lost: 2, awake_share_of_loss: 2 / 12 }) })) } });
    renderCard();
    expect(await screen.findByText(/Of the 12% your truck lost while parked over 30 days, about 2% was while it was awake/)).toBeInTheDocument();
    expect(screen.queryByText(/needs about two days of parked history/)).toBeNull();
  });
});
