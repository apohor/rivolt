import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

// /signup for someone who is already signed in: no account creation; on to the connect step (no truck yet) or home.
const me = { user_id: "u1", username: "a@b.c", role: "user", onboarding_completed: true };

function Where() {
  const l = useLocation();
  return <p data-testid="where">{l.pathname + l.search}</p>;
}

async function mountSignup(path: string, opts: { signedIn: boolean; rivian?: { enabled: boolean; authenticated: boolean } }) {
  vi.resetModules();
  stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, demo_enabled: true, design_preview: [] } } },
    "/api/auth/oidc": { body: [] },
    "/api/auth/me": { body: opts.signedIn ? me : { authenticated: false } },
    "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false, ...opts.rivian } },
  });
  const { default: SignupPage } = await import("./SignupPage");
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/signup" element={<SignupPage />} />
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("/signup while signed in", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("no truck yet: goes to the connect step", async () => {
    await mountSignup("/signup", { signedIn: true });
    expect((await screen.findByTestId("where")).textContent).toBe("/onboarding?connect=1");
  });

  it("truck connected: goes home", async () => {
    await mountSignup("/signup", { signedIn: true, rivian: { enabled: true, authenticated: true } });
    expect((await screen.findByTestId("where")).textContent).toBe("/");
  });

  it("Rivian connections off on this install: goes home, not to a connect step that is not there", async () => {
    await mountSignup("/signup", { signedIn: true, rivian: { enabled: false, authenticated: false } });
    expect((await screen.findByTestId("where")).textContent).toBe("/");
  });

  it("signed out: the sign-up page stays", async () => {
    await mountSignup("/signup", { signedIn: false });
    expect(await screen.findByPlaceholderText("you@example.com")).toBeTruthy();
    expect(screen.queryByTestId("where")).toBeNull();
  });

  it("a sign-up link (?token) is a new account on purpose: the page stays even when signed in", async () => {
    await mountSignup("/signup?token=T1", { signedIn: true });
    expect(await screen.findByRole("heading", { level: 1 })).toBeTruthy();
    await new Promise((r) => setTimeout(r, 100));
    expect(screen.queryByTestId("where")).toBeNull();
  });
});
