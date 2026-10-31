import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { ApiError } from "../lib/api";
import { proFeatureText, proLimitOf } from "../lib/proLimit";
import { PlanError } from "../pages/TripPlanPage";
import { PrimaryVehicleCard } from "./PrimaryVehicleCard";
import { stubFetchRoutes } from "../test/fetchRoutes";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import billingOff from "../../../ios/RivoltTests/Fixtures/me.json";
import stripe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const two = [
  { id: "veh-a", vin: "A", name: "Trail", model: "R1T" },
  { id: "veh-b", vin: "B", name: "Family", model: "R1S", pro_locked: true },
];

describe("the free plan's limits on the client", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("a multi-day plan's 402 says what is Pro and that a single day stays free", () => {
    // jsdom has no scrollIntoView; the error box scrolls itself into view.
    Element.prototype.scrollIntoView = vi.fn();
    const err = new ApiError(402, { error: "pro_required" });
    wrap(<PlanError error={err} />);
    expect(screen.getByText(/Multi-day plans, overnights and re-plan from here are part of Rivolt Pro\. A single-day plan stays free\./)).toBeInTheDocument();
  });

  it("reads the saved-trip cap from the 402 and says what stays", () => {
    const l = proLimitOf(new ApiError(402, { error: "pro_required", limit: 3 }));
    expect(l).toEqual({ code: "pro_required", limit: 3 });
    expect(proFeatureText("saved-trips", 3)).toMatch(/keeps 3 saved trips.*open and delete them/);
    expect(proFeatureText("plan-vs-actual")).toMatch(/kept either way/);
    expect(proFeatureText("vehicle")).toMatch(/keeps recording and alerting.*export/);
  });

  it("the primary-vehicle card shows for a free account with two vehicles and saves the choice", async () => {
    const f = stubFetchRoutes({
      "/api/auth/me": { body: free },
      "/api/vehicles": { body: two },
      "/api/settings/primary-vehicle": { body: { vehicle_id: "veh-b" } },
    });
    wrap(<PrimaryVehicleCard />);
    expect(await screen.findByText("The free plan shows one vehicle. The others keep recording and alerting, stay in your export, and Pro shows them all. Nothing is deleted.")).toBeInTheDocument();
    const go = screen.getByRole("button", { name: "Show this vehicle" });
    expect(go).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Family"));
    expect(go).toBeEnabled();
    fireEvent.click(go);
    await waitFor(() => expect(f.mock.calls.some((c) => (c[1] as RequestInit | undefined)?.method === "PUT")).toBe(true));
    const put = f.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === "PUT")!;
    expect(JSON.parse(String((put[1] as RequestInit).body))).toEqual({ vehicle_id: "veh-b" });
    expect(await screen.findByText("Done. The free plan now shows this vehicle.")).toBeInTheDocument();
  });

  it("the card is absent where billing is off, for Pro and for a single vehicle", async () => {
    for (const [me, vehicles] of [[billingOff, two], [stripe, two], [free, [two[0]]]] as const) {
      stubFetchRoutes({ "/api/auth/me": { body: me }, "/api/vehicles": { body: vehicles } });
      const { container, unmount } = wrap(<PrimaryVehicleCard />);
      await new Promise((r) => setTimeout(r, 30));
      expect(container.querySelector("[data-primary-vehicle]")).toBeNull();
      unmount();
    }
  });
});
