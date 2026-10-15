import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import DemoPage from "./DemoPage";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderDemo() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <DemoPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("DemoPage", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows the sample banner and the Now card for the demo vehicle", async () => {
    stubFetchRoutes({
      "/api/demo": { body: { enabled: true, vehicle_id: "demo-1", home_label: "Sample lot" } },
      "/api/vehicles": { body: [{ id: "demo-1", name: "Sample R1S", pack_kwh: 135 }] },
    });
    renderDemo();
    expect(await screen.findByText(/Sample truck\./)).toBeInTheDocument();
    // Sign-ups open by default: the CTA goes to signup.
    expect(await screen.findByRole("link", { name: "Connect with a driver account" })).toHaveAttribute("href", "/signup");
    expect(await screen.findByRole("region", { name: "Now" })).toBeInTheDocument();
    // /live needs a session: no dead link in the demo.
    expect(screen.queryByRole("link", { name: /Full telemetry/ })).toBeNull();
  });

  it("says so when the demo is off", async () => {
    stubFetchRoutes({}); // /api/demo → 404
    renderDemo();
    expect(await screen.findByText(/demo isn't available/)).toBeInTheDocument();
  });
});
