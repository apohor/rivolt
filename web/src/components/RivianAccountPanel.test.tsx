import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
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

  it("shows when the connection expires, as Connected · expires Nov 3", async () => {
    stubFetchRoutes({
      "/api/settings/rivian": { body: { enabled: true, authenticated: true, mfa_pending: false, email: "d@example.com", session_expires_at: "2026-11-03T18:00:00Z", session_days_left: 5 } },
      "/api/vehicles/owned": { body: { vehicles: [] } },
    });
    renderPanel();
    expect(await screen.findByText("Connected · expires Nov 3")).toBeInTheDocument();
  });

  it("says nothing about expiry when the server sends no date", async () => {
    stubFetchRoutes({
      "/api/settings/rivian": { body: { enabled: true, authenticated: true, mfa_pending: false, email: "d@example.com" } },
      "/api/vehicles/owned": { body: { vehicles: [] } },
    });
    renderPanel();
    await screen.findByText("Connected as");
    expect(screen.queryByText(/expires/)).not.toBeInTheDocument();
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

describe("a status refetch does not take the form away", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });
  // The refetch lands, then React renders what it says (a beat later), so the checks below run after both.
  const refetch = async (qc: QueryClient) => {
    await act(async () => {
      await qc.refetchQueries({ queryKey: ["rivian", "status"] });
      await new Promise((r) => setTimeout(r, 100));
    });
  };
  const wrap = () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(
      <QueryClientProvider client={qc}>
        <RivianAccountPanel />
      </QueryClientProvider>,
    );
    return { qc, view };
  };

  it("keeps the sign-in form and what was typed when a refetch says connected", async () => {
    stubFetchRoutes({ "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false } } });
    const { qc } = wrap();
    const email = await screen.findByPlaceholderText("you@example.com");
    fireEvent.change(email, { target: { value: "me@example.com" } });
    fireEvent.change(screen.getByPlaceholderText("Password"), { target: { value: "hunter2" } });
    // Another server answers the next poll: connected, with a stale needs_reauth.
    stubFetchRoutes({
      "/api/settings/rivian": { body: { enabled: true, authenticated: true, mfa_pending: false, email: "me@example.com", needs_reauth: true } },
      "/api/vehicles/owned": { body: { vehicles: [] } },
    });
    await refetch(qc);
    expect(qc.getQueryData(["rivian", "status"])).toMatchObject({ authenticated: true });
        expect(screen.getByPlaceholderText("you@example.com")).toHaveValue("me@example.com");
    expect(screen.getByPlaceholderText("Password")).toHaveValue("hunter2");
    expect(screen.queryByText("Rivian session expired")).not.toBeInTheDocument();
  });

  it("keeps the form when a refetch fails, instead of an error box", async () => {
    stubFetchRoutes({ "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false } } });
    const { qc } = wrap();
    fireEvent.change(await screen.findByPlaceholderText("you@example.com"), { target: { value: "me@example.com" } });
    stubFetchRoutes({ "/api/settings/rivian": { status: 502, body: "bad gateway" } });
    await refetch(qc);
    expect(qc.getQueryState(["rivian", "status"])?.status).toBe("error");
    expect(screen.getByPlaceholderText("you@example.com")).toHaveValue("me@example.com");
    expect(screen.queryByText(/Couldn't load Rivian status/)).not.toBeInTheDocument();
  });

  it("still shows the connected view when nothing is being typed", async () => {
    stubFetchRoutes({ "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false } } });
    const { qc } = wrap();
    await screen.findByPlaceholderText("you@example.com");
    stubFetchRoutes({
      "/api/settings/rivian": { body: { enabled: true, authenticated: true, mfa_pending: false, email: "me@example.com" } },
      "/api/vehicles/owned": { body: { vehicles: [] } },
    });
    await refetch(qc);
    expect(await screen.findByText("Connected as")).toBeInTheDocument();
  });

  it("shows the session-expired notice on a connected account when no sign-in just happened", async () => {
    stubFetchRoutes({
      "/api/settings/rivian": { body: { enabled: true, authenticated: true, mfa_pending: false, email: "me@example.com", needs_reauth: true } },
      "/api/vehicles/owned": { body: { vehicles: [] } },
    });
    wrap();
    expect(await screen.findByText("Rivian session expired")).toBeInTheDocument();
  });
});
