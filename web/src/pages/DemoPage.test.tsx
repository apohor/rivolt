import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import DemoLayout from "../layout/DemoLayout";
import DemoIndex from "./DemoPage";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderDemo(path = "/demo") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="demo" element={<DemoLayout />}>
            <Route index element={<DemoIndex home={<div>home page</div>} />} />
            <Route path="money" element={<div>money page</div>} />
            <Route path="activity" element={<div>activity page</div>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("demo shell", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows the sample banner, the app's tab nav under /demo, and the page", async () => {
    stubFetchRoutes({
      "/api/demo": { body: { enabled: true, vehicle_id: "demo-1", home_label: "Sample lot" } },
    });
    renderDemo();
    expect(await screen.findByText(/Sample truck\./)).toBeInTheDocument();
    // Sign-ups open by default: the CTA goes to signup.
    expect(await screen.findByRole("link", { name: "Connect with a driver account" })).toHaveAttribute("href", "/signup");
    expect(await screen.findByText("home page")).toBeInTheDocument();
    for (const [name, href] of [
      ["Now", "/demo"],
      ["Activity", "/demo/activity"],
      ["Money", "/demo/money"],
      ["Plan", "/demo/trips/plan"],
      ["Truck", "/demo/truck"],
    ] as const) {
      expect(screen.getAllByRole("link", { name })[0]).toHaveAttribute("href", href);
    }
  });

  it("sends old /demo?tab= links to the real routes", async () => {
    stubFetchRoutes({ "/api/demo": { body: { enabled: true, vehicle_id: "demo-1", home_label: "x" } } });
    renderDemo("/demo?tab=money");
    expect(await screen.findByText("money page")).toBeInTheDocument();
  });

  it("says so when the demo is off", async () => {
    stubFetchRoutes({}); // /api/demo → 404
    renderDemo();
    expect(await screen.findByText(/demo isn't available/)).toBeInTheDocument();
  });
});
