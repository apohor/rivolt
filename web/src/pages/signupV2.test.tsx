import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

// config.ts caches /api/config at module level, so each case gets a fresh module graph.
async function mount(file: "SignupPage" | "OnboardingPage" | "DemoBanner", keys: string[], opts: { demo?: boolean; connected?: boolean } = {}) {
  vi.resetModules();
  stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, demo_enabled: !!opts.demo, design_preview: keys } } },
    "/api/auth/oidc": { body: [{ name: "google", display_name: "Google", start_url: "/auth/google" }] },
    "/api/auth/me": { status: 401, body: {} },
    "/api/settings/rivian": { body: { authenticated: !!opts.connected, email: "a@b.c" } },
  });
  const mod = await import(file === "DemoBanner" ? "../components/DemoBanner" : `./${file}`);
  const Page = file === "DemoBanner" ? mod.DemoBanner : mod.default;
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <Page />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("signup_v2 copy", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("sign-up, key on: providers, email, sample-truck link, no GitHub walkthrough", async () => {
    await mount("SignupPage", ["signup_v2"], { demo: true });
    expect(await screen.findByRole("heading", { name: "Create your Rivolt account" })).toBeTruthy();
    expect(await screen.findByRole("button", { name: /Google/ })).toBeTruthy();
    expect(screen.getByText("or with your email")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Email me a sign-up link" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Try a sample truck" })).toHaveAttribute("href", "/demo");
    expect(screen.queryByRole("link", { name: "signup walkthrough" })).toBeNull();
    expect(screen.queryByText(/Live telemetry/)).toBeNull();
  });

  it("sign-up, key off: today's screen", async () => {
    await mount("SignupPage", [], { demo: true });
    expect(await screen.findByRole("heading", { name: "Create your account" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send signup link" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "signup walkthrough" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Try a sample truck" })).toBeNull();
  });

  it("demo banner: key on says Add my truck, key off keeps the old button", async () => {
    await mount("DemoBanner", ["signup_v2"]);
    expect(await screen.findByRole("link", { name: "Add my truck" })).toHaveAttribute("href", "/signup");
    cleanup();
    await mount("DemoBanner", []);
    expect(await screen.findByRole("link", { name: "Connect with a driver account" })).toBeTruthy();
  });

  it("onboarding, connected: the finish button names where it goes only with the key on", async () => {
    await mount("OnboardingPage", ["signup_v2"], { connected: true });
    expect(await screen.findByRole("button", { name: "See my truck" })).toBeTruthy();
    cleanup();
    await mount("OnboardingPage", [], { connected: true });
    expect(await screen.findByRole("button", { name: "Go to Overview" })).toBeTruthy();
  });

  it("onboarding, not connected: key on opens on the choice, key off on the connect form", async () => {
    await mount("OnboardingPage", ["signup_v2"], { demo: true });
    expect(await screen.findByRole("heading", { name: "Your account is ready" })).toBeTruthy();
    const sample = screen.getByRole("link", { name: /Try it with a sample truck/ });
    expect(sample.getAttribute("href")).toBe("/demo?from=app");
    expect(screen.queryByRole("heading", { name: "Connect your Rivian" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Add my truck/ }));
    expect(await screen.findByRole("heading", { name: "Connect your Rivian" })).toBeTruthy();
    cleanup();
    await mount("OnboardingPage", [], { demo: true });
    expect(await screen.findByRole("heading", { name: "Connect your Rivian" })).toBeTruthy();
    expect(screen.queryByText("Your account is ready")).toBeNull();
  });

  it("demo banner: the way back shows only with the key on and ?from=app, and survives navigation", async () => {
    window.history.pushState({}, "", "/demo?from=app");
    await mount("DemoBanner", ["signup_v2"], { demo: true });
    expect(await screen.findByRole("link", { name: "Back to my account" })).toBeTruthy();
    cleanup();
    window.history.pushState({}, "", "/demo/drives");
    await mount("DemoBanner", ["signup_v2"], { demo: true });
    expect(await screen.findByRole("link", { name: "Back to my account" })).toBeTruthy();
    cleanup();
    sessionStorage.clear();
    window.history.pushState({}, "", "/demo?from=app");
    await mount("DemoBanner", [], { demo: true });
    await screen.findByText(/Generated data/);
    expect(screen.queryByRole("link", { name: "Back to my account" })).toBeNull();
    sessionStorage.clear();
  });
});
