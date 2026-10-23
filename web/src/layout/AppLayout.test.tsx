import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import AppLayout from "./AppLayout";
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

describe("AppLayout", () => {
  beforeEach(() => {
    Object.defineProperty(window, "location", {
      value: { pathname: "/", search: "", assign: vi.fn() },
      writable: true,
    });
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("tab-bar shell with Money, and a Truck sub-nav on /settings", async () => {
    const fetch = stubFetchRoutes(common);
    renderLayout("/settings");
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
    // No flag to ask for any more.
    expect(fetch.mock.calls.some(([u]) => String(u).startsWith("/api/ledger/enabled"))).toBe(false);
  });
});
