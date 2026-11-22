import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

async function mount(page: "onboarding" | "prompt", opts: { keys: string[]; connected: boolean; demo?: boolean }) {
  vi.resetModules();
  stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, demo_enabled: !!opts.demo, design_preview: opts.keys } } },
    "/api/auth/me": { body: { id: "u", email: "a@b.c", onboarding_completed: false } },
    "/api/settings/rivian": { body: { enabled: true, authenticated: opts.connected, email: "a@b.c" } },
    "/api/vehicles/owned": { body: { vehicles: [{ id: "v1", name: "Truck" }] } },
    "/api/events": { body: {} },
  });
  const Page = page === "onboarding" ? (await import("../pages/OnboardingPage")).default : (await import("../components/ConnectRivianPrompt")).default;
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={["/onboarding?connect=1"]}>
        {page === "onboarding" ? <Page /> : <Page inline />}
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("signup_v2 connected step", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("key on: What happens next with the code's times, then See my truck", async () => {
    await mount("onboarding", { keys: ["signup_v2"], connected: true });
    const h = await screen.findByRole("heading", { name: /What happens next/ });
    expect(document.activeElement).toBe(h);
    const card = document.querySelector("[data-what-next]") as HTMLElement;
    expect(card.textContent).toContain("battery, range and where it's parked show on Now");
    expect(card.textContent).toContain("about 10 seconds after you park");
    expect(card.textContent).toContain("not pulled from Rivian");
    expect(screen.getByRole("button", { name: "See my truck" })).toBeTruthy();
  });

  it("key off: no card", async () => {
    await mount("onboarding", { keys: [], connected: true });
    await screen.findByRole("button", { name: "Go to Overview" });
    expect(document.querySelector("[data-what-next]")).toBeNull();
  });

  it("Now prompt: sample truck one tap away until connected, key on only", async () => {
    await mount("prompt", { keys: ["signup_v2"], connected: false, demo: true });
    expect((await screen.findByRole("link", { name: "Try it with a sample truck first" })).getAttribute("href")).toBe("/demo?from=app");
  });

  it("Now prompt: key off has no such link", async () => {
    await mount("prompt", { keys: [], connected: false, demo: true });
    await screen.findByText("Connect your Rivian to get started");
    expect(document.querySelector("[data-sample-truck]")).toBeNull();
  });
});
