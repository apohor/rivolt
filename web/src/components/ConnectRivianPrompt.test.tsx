import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import ConnectRivianPrompt from "./ConnectRivianPrompt";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderPrompt(inline: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ConnectRivianPrompt inline={inline} context="Once connected, drives appear here." />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("connected, but the account has no vehicle", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });
  const noVehicle = {
    "/api/settings/rivian": { body: { enabled: true, authenticated: true, mfa_pending: false, email: "d@example.com" } },
    "/api/vehicles/owned": { body: { vehicles: [] } },
  };

  it("the compact prompt on the other screens says the usual cause and what to do", async () => {
    stubFetchRoutes(noVehicle);
    renderPrompt(false);
    expect(await screen.findByText("Connected, but no vehicles found")).toBeInTheDocument();
    expect(screen.getByText(/Usual cause: a driver account whose invite isn't accepted yet/)).toBeInTheDocument();
    expect(screen.getByText(/Connected your main account instead\?/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Fix in Settings" })).toBeInTheDocument();
  });

  it("the inline prompt on Now says it through the account panel, with Check again", async () => {
    stubFetchRoutes(noVehicle);
    renderPrompt(true);
    expect(await screen.findByText(/Usual cause: a driver account whose invite isn't accepted yet/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check again" })).toBeInTheDocument();
  });
});
