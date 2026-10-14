import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import AppLayout from "./AppLayout";
import { _resetMoneyTabFlagForTests } from "../lib/ledger";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderLayout(path = "/") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route element={<AppLayout />}>
            <Route index element={<div>home page</div>} />
            <Route path="drives" element={<div>drives page</div>} />
            <Route path="settings" element={<div>settings page</div>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const common = {
  "/api/health": { body: { ok: true, version: "test" } },
  "/api/settings/rivian": { body: { enabled: true, authenticated: true } },
  "/api/auth/me": { body: { user_id: "u1", username: "owner", role: "user", onboarding_completed: true } },
};

describe("AppLayout money_tab_enabled gate", () => {
  beforeEach(() => {
    _resetMoneyTabFlagForTests();
    Object.defineProperty(window, "location", {
      value: { pathname: "/", search: "", assign: vi.fn() },
      writable: true,
    });
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("flag off: today's top nav, no Money tab", async () => {
    const fetch = stubFetchRoutes(common); // /api/ledger/enabled → 404
    renderLayout();
    expect(await screen.findByText("home page")).toBeInTheDocument();
    await vi.waitFor(() =>
      expect(fetch.mock.calls.some(([u]) => String(u).startsWith("/api/ledger/enabled"))).toBe(true),
    );
    const nav = screen.getByRole("navigation", { name: "Primary" });
    expect(within(nav).getByRole("link", { name: "Overview" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Money/ })).toBeNull();
  });

  it("flag on: tab-bar shell with Money, and a Truck sub-nav on /settings", async () => {
    stubFetchRoutes({ ...common, "/api/ledger/enabled": { body: { enabled: true } } });
    renderLayout("/settings");
    // The flag resolves after first paint, so the shell swaps in once
    // /api/ledger/enabled answers; wait for it before asserting.
    await screen.findByRole("navigation", { name: "Truck sections" });
    expect(screen.getByText("settings page")).toBeInTheDocument();
    const navs = screen.getAllByRole("navigation", { name: "Primary" });
    // Rail (desktop) + bottom bar (mobile); CSS decides which shows.
    expect(navs).toHaveLength(2);
    for (const nav of navs) {
      expect(within(nav).getByRole("link", { name: /Money/ })).toHaveAttribute("href", "/money");
      expect(within(nav).getByRole("link", { name: /Truck/ })).toHaveAttribute("aria-current", "page");
    }
    const sub = screen.getByRole("navigation", { name: "Truck sections" });
    expect(within(sub).getByRole("link", { name: "Settings" })).toHaveAttribute("href", "/settings");
    // The header carries no connection pill or Sign out in the shell.
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull();
  });
});
