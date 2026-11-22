import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { TrendsCard } from "./TrendsCard";
import { stubFetchRoutes } from "../test/fetchRoutes";
import required from "../../../ios/RivoltTests/Fixtures/err_pro_required.json";

const trends = {
  units: "imperial", distance_unit: "mi", efficiency_unit: "mi/kWh", timezone: "UTC", months: 12, min_month_distance: 50, min_months: 3, qualifying_months: 1,
  efficiency: [{ month: "2026-10", distance: 400, energy_kwh: 130, efficiency: 3.1, drives: 20 }],
  capacity: null,
};

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <TrendsCard vehicleID="v1" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const asked = (fetch: ReturnType<typeof stubFetchRoutes>) =>
  fetch.mock.calls.map((c) => String(c[0])).filter((u) => u.includes("/trends")).map((u) => new URL(u, "http://x").searchParams.get("months"));

describe("TrendsCard range", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("a Pro account starts at 12 months and can ask for 24 months or all", async () => {
    const fetch = stubFetchRoutes({ "/api/vehicles/v1/trends": { body: trends } });
    renderCard();
    const group = await screen.findByRole("group", { name: "Range" });
    expect(group).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "12 months" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByRole("button", { name: "24 months" }));
    await waitFor(() => expect(asked(fetch)).toContain("24"));
    expect(screen.getByRole("button", { name: "24 months" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => expect(asked(fetch)).toContain("120"));
    expect(asked(fetch)[0]).toBe("12");
  });

  it("a free account's 402 shows the Pro notice, no range control, and never asks for more than 12", async () => {
    const fetch = stubFetchRoutes({ "/api/vehicles/v1/trends": { status: 402, body: required } });
    renderCard();
    await waitFor(() => expect(document.querySelector("[data-pro-limit]")).not.toBeNull());
    expect(screen.queryByRole("group", { name: "Range" })).toBeNull();
    expect(asked(fetch)).toEqual(["12"]);
  });
});
