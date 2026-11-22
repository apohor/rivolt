import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

async function mount(page: "onboarding" | "prompt", opts: { keys: string[]; connected: boolean; demo?: boolean; inline?: boolean }) {
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
        {page === "onboarding" ? <Page /> : <Page inline={opts.inline ?? true} context="Once connected, drives appear here automatically." />}
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

  it("Now prompt, key on: the one short form, one sample-truck link, none of the long copy", async () => {
    await mount("prompt", { keys: ["signup_v2"], connected: false, demo: true });
    expect((await screen.findByRole("link", { name: "Try a sample truck" })).getAttribute("href")).toBe("/demo?from=app");
    expect(screen.getAllByRole("link", { name: /sample truck/i })).toHaveLength(1);
    expect(screen.getAllByRole("heading", { name: "Connect your Rivian" })).toHaveLength(1);
    expect(screen.getByLabelText("Rivian email")).toBeTruthy();
    expect(document.querySelector("[data-connect-trust]")).toBeNull();
    expect(document.body.textContent).not.toContain("Once connected, drives, charges");
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("Drives, Charges and Plan prompt, key on: title, one line, button; no trust copy, no sample card", async () => {
    await mount("prompt", { keys: ["signup_v2"], connected: false, demo: true, inline: false });
    expect(await screen.findByRole("heading", { name: "Connect your Rivian" })).toBeTruthy();
    expect(screen.getByText("Once connected, drives appear here automatically.")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Connect Rivian" }).getAttribute("href")).toBe("/settings?tab=account#rivian");
    expect(document.body.textContent).not.toMatch(/sample truck|sends no commands/i);
  });

  it("Now prompt: key off has no such link", async () => {
    await mount("prompt", { keys: [], connected: false, demo: true });
    await screen.findByText("Connect your Rivian to get started");
    expect(document.querySelector("[data-sample-truck]")).toBeNull();
  });
});
