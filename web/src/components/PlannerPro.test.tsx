import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { ProLimitNotice } from "./ProLimitNotice";
import { stubFetchRoutes } from "../test/fetchRoutes";
import { PLANNER_ALL_PRO, PLANNER_ALL_PRO_SAVED } from "../lib/proLimit";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import gated from "../../../ios/RivoltTests/Fixtures/me_plan_free_planner_pro.json";

// A free account created after the planner cutoff (me.planner_pro_only): the planner note names the whole planner, not just the multi-day part.
function wrap(hasSavedTrips = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ProLimitNotice limit={{ code: "pro_required" }} feature="planner" hasSavedTrips={hasSavedTrips} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("planner Pro (planner_pro_only)", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("an account created after the cutoff is told trip planning is Pro, with the Pro entry", async () => {
    stubFetchRoutes({ "/api/auth/me": { body: gated } });
    wrap();
    expect(await screen.findByText(PLANNER_ALL_PRO)).toBeInTheDocument();
    expect(screen.queryByText(/single-day plan stays free/)).not.toBeInTheDocument();
    expect(await screen.findByRole("button", { name: /See Pro/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "See the trip planner in the demo" })).toBeInTheDocument();
  });

  it("the saved-trips sentence shows only when the account has saved trips", async () => {
    stubFetchRoutes({ "/api/auth/me": { body: gated } });
    wrap(true);
    expect(await screen.findByText(PLANNER_ALL_PRO_SAVED)).toBeInTheDocument();
    cleanup();
    wrap(false);
    expect(await screen.findByText(PLANNER_ALL_PRO)).toBeInTheDocument();
    expect(screen.queryByText(/saved earlier|you saved earlier/)).not.toBeInTheDocument();
  });

  it("an older free account keeps the multi-day sentence", async () => {
    stubFetchRoutes({ "/api/auth/me": { body: free } });
    wrap();
    expect(await screen.findByText(/A single-day plan stays free\./)).toBeInTheDocument();
    expect(screen.queryByText(PLANNER_ALL_PRO)).not.toBeInTheDocument();
  });
});
