import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { stubFetchRoutes } from "../test/fetchRoutes";

// signup_v2: Settings > Rivian account shows the same short card as Now and onboarding (no radios, no authorized_driver); key off keeps the old form.
async function mount(keys: string[]) {
  vi.resetModules();
  stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, demo_enabled: false, design_preview: keys } } },
    "/api/auth/me": { body: { id: "u", email: "a@b.c", onboarding_completed: true } },
    "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false } },
    "/api/events": { body: {} },
  });
  const Settings = (await import("./SettingsPage")).default;
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={["/settings?tab=account"]}>
        <Settings />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Settings Rivian account form", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("key on: the short card, no account-kind radios", async () => {
    await mount(["signup_v2"]);
    expect(await screen.findByRole("button", { name: "Sign in to Rivian" })).toBeTruthy();
    expect(screen.queryByRole("radiogroup", { name: "Which account" })).toBeNull();
    expect(screen.queryByText(/Why a driver account/)).toBeNull();
  });

  it("key off: the old form with its radios", async () => {
    await mount([]);
    expect(await screen.findByRole("radiogroup", { name: "Which account" })).toBeTruthy();
  });
});
