import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RivianAccountPanel } from "./RivianAccountPanel";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <RivianAccountPanel />
    </QueryClientProvider>,
  );
}

describe("connect step: driver account first", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });
  const notConnected = { "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false } } };

  it("preselects the driver account, keeps the main account, and explains why", async () => {
    stubFetchRoutes(notConnected);
    renderPanel();
    expect(await screen.findByText(/We recommend a dedicated driver account/)).toBeInTheDocument();
    const driver = screen.getByRole("radio", { name: /dedicated driver account/ });
    const main = screen.getByRole("radio", { name: /main Rivian account/ });
    expect(driver).toBeChecked();
    expect(main).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Sign in with driver account" })).toBeInTheDocument();
    expect(screen.getByText("Recommended: a dedicated driver account.")).toBeInTheDocument();
    // "Why?" opens the honest case, trade-offs included.
    fireEvent.click(screen.getByRole("button", { name: /Why a driver account/ }));
    expect(screen.getByText(/The honest trade-offs:/)).toBeInTheDocument();
    expect(screen.getByText(/Rivian can change or block access at any time/)).toBeInTheDocument();
  });

  it("switching to the main account changes the helper and button", async () => {
    stubFetchRoutes(notConnected);
    renderPanel();
    fireEvent.click(await screen.findByRole("radio", { name: /main Rivian account/ }));
    expect(screen.getByText("You can switch to a driver account later.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
    // The helper and button say "driver account"; the plain-terms panel above the form is where
    // "Authorized Driver" is explained.
    expect(screen.getByText("You can switch to a driver account later.").textContent).not.toMatch(/Authorized Driver/);
    expect(screen.getByRole("button", { name: "Sign in" }).textContent).not.toMatch(/Authorized Driver/);
  });

  it("uses growth's copy for an account with no vehicles", async () => {
    stubFetchRoutes({
      "/api/settings/rivian": { body: { enabled: true, authenticated: true, mfa_pending: false, email: "d@example.com" } },
      "/api/vehicles/owned": { body: { vehicles: [] } },
    });
    renderPanel();
    expect(await screen.findByText(/Usual cause: a driver account whose invite isn't accepted yet/)).toBeInTheDocument();
    expect(screen.getByText(/Connected your main account instead\?/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check again" })).toBeInTheDocument();
  });
});
