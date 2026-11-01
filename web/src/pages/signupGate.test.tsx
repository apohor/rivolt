import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

// config.ts caches /api/config at module level, so each case gets a
// fresh module graph.
async function mount(signupEnabled: boolean | undefined, path: "/login" | "/signup", query = "") {
  vi.resetModules();
  const features: Record<string, boolean> = { trip_planner_enabled: false };
  if (signupEnabled !== undefined) features.signup_enabled = signupEnabled;
  stubFetchRoutes({
    "/api/config": { body: { features } },
    "/api/auth/oidc/providers": { body: [] },
    "/api/auth/me": { status: 401, body: {} },
  });
  const Page = (await import(path === "/login" ? "./LoginPage" : "./SignupPage")).default;
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[path + query]}>
        <Page />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("sign-up closed (RIVOLT_SIGNUP_DISABLED)", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("login shows the Create an account link when sign-ups are open", async () => {
    await mount(true, "/login");
    expect(await screen.findByRole("link", { name: /create an account/i })).toBeTruthy();
  });

  it("login treats a server that omits the flag as open", async () => {
    await mount(undefined, "/login");
    expect(await screen.findByRole("link", { name: /create an account/i })).toBeTruthy();
  });

  it("login hides the link when sign-ups are closed", async () => {
    await mount(false, "/login");
    await screen.findByText(/Rivolt/i);
    // Wait until /api/config has actually been applied, so the absence
    // below isn't just "not loaded yet".
    const { ensureConfig } = await import("../lib/config");
    expect((await ensureConfig()).features.signupEnabled).toBe(false);
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole("link", { name: /create an account/i })).toBeNull();
  });

  it("login explains a refused first sign-in", async () => {
    await mount(false, "/login", "?error=signups_disabled");
    expect(await screen.findByText(/Sign-ups are closed on this server/i)).toBeTruthy();
  });

  it("/signup shows the closed notice instead of the form", async () => {
    await mount(false, "/signup");
    expect(await screen.findByText(/Sign-ups are closed on this server/i)).toBeTruthy();
    expect(screen.queryByLabelText(/password/i)).toBeNull();
  });
});

describe("no Beta wording on the sign-in and sign-up cards (owner, Nov 1)", () => {
  beforeEach(() => vi.resetModules());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("login has no Beta pill", async () => {
    await mount(true, "/login");
    await screen.findByRole("link", { name: /create an account/i });
    expect(screen.queryByText("Beta")).toBeNull();
  });

  it("sign-up has no Beta pill and says it is an open-source Rivian companion, not that it is in closed beta", async () => {
    await mount(true, "/signup");
    expect(await screen.findByText(/Rivolt is an open-source Rivian companion\./)).toBeTruthy();
    expect(screen.queryByText("Beta")).toBeNull();
    expect(document.body.textContent).not.toMatch(/closed beta/i);
  });
});

