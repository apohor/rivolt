import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CoachCard } from "./CoachCard";
import { coachNeedsText, type CoachResponse } from "../lib/savings";

const bucket = { sessions: 0, kwh: 0, cost: 0, estimated: false, estimated_cost_share: 0 };
const full: CoachResponse = {
  version: 1,
  currency: "USD",
  months: ["2026-08", "2026-10"],
  cost_per_kwh: { home: { ...bucket, sessions: 4, per_kwh: 0.21, estimated: true }, public: bucket, fast_charging: { ...bucket, sessions: 2, per_kwh: 0.5 }, fast_premium_pct: 138, estimated: true },
  cost_per_100: { per_100_mi: 5, per_100_km: 3.11, spend: 10, miles: 200, unpriced_share: 0, estimated: false },
  off_peak_shift: { needs: "flat_rate", sessions_moved: 0, sessions_considered: 0, estimated: true },
};

function show(d: CoachResponse) {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(d), { status: 200, headers: { "content-type": "application/json" } }));
  render(
    <QueryClientProvider client={new QueryClient()}>
      <CoachCard />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("CoachCard", () => {
  it("labels guessed prices as estimated and shows a missing input as a sentence", async () => {
    show(full);
    expect(await screen.findByText(/Home: \$0\.210 per kWh/)).toBeTruthy();
    expect(screen.getAllByText(/\(estimated\)/).length).toBeGreaterThan(0);
    expect(screen.getByText(/flat rate/i)).toBeTruthy();
    // Fast charging was not estimated: no label on its own line.
    expect(screen.getByText(/Fast charging: \$0\.500 per kWh/).textContent).not.toMatch(/estimated/);
  });
  it("never shows a figure for cost per 100 when the server withheld it", async () => {
    show({ ...full, cost_per_100: { needs: "unpriced_charges", spend: 0, miles: 300, unpriced_share: 0.4, estimated: false } });
    expect(await screen.findByText(/no price yet/)).toBeTruthy();
    expect(screen.queryByText(/\$[0-9.]+ per 100 mi\b/)).toBeNull();
  });
  it("has a sentence for every needs value", () => {
    for (const n of ["priced_charges", "more_driving", "unpriced_charges", "flat_rate", "home_rate", "offpeak_schedule", "more_home_sessions"]) expect(coachNeedsText(n).length).toBeGreaterThan(20);
  });
});
