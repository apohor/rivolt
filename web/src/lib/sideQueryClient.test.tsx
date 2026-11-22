import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { BrowserRouter, Link, Route, Routes, useNavigate } from "react-router-dom";
import { QueryClient, useQuery } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";
import { SideQueryProvider } from "./sideQueryClient";

// The sample truck's cached answers must never be read by the real app, nor the reverse, whichever way the visitor crosses.
const me = { user_id: "u1", username: "a@b.c", role: "user", onboarding_completed: true };

// Reads the Rivian status the way the connect step does: through the shared query key.
function Status() {
  const q = useQuery({ queryKey: ["rivian", "status"], queryFn: async () => (await import("./api")).backend.rivianStatus() });
  return <p data-testid="status">{q.data ? `enabled:${q.data.enabled}` : "loading"}</p>;
}

let nav: ReturnType<typeof useNavigate>;
function Nav() {
  nav = useNavigate();
  return <Link to="/signup">to signup</Link>;
}

async function mountAt(path: string) {
  window.history.pushState({}, "", path);
  stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, demo_enabled: true, design_preview: [] } } },
    "/api/auth/oidc": { body: [] },
    "/api/auth/me": { body: me },
    "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false } },
  });
  vi.resetModules();
  const { default: SignupPage } = await import("../pages/SignupPage");
  // The app's one real client, as main.tsx builds it.
  const appClient = new QueryClient({ defaultOptions: { queries: { staleTime: 30_000, retry: false } } });
  return render(
    <BrowserRouter>
      <SideQueryProvider appClient={appClient}>
        <Nav />
        <Routes>
          <Route path="/signup" element={<SignupPage />} />
          <Route path="*" element={<Status />} />
        </Routes>
      </SideQueryProvider>
    </BrowserRouter>,
  );
}

describe("demo and app caches", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    window.history.pushState({}, "", "/");
  });

  it("demo, then the app inside 30 s: the app asks the server, not the demo's cached answer", async () => {
    await mountAt("/demo");
    await screen.findByText("enabled:false");
    await act(async () => nav("/onboarding?connect=1"));
    await vi.waitFor(() => expect(screen.getByTestId("status").textContent).toBe("enabled:true"));
  });

  it("signed in, no truck, no from-app mark: demo to /signup to the connect step shows the real answer", async () => {
    await mountAt("/demo");
    await screen.findByText("enabled:false");
    await act(async () => screen.getByText("to signup").click());
    await vi.waitFor(() => expect(window.location.pathname + window.location.search).toBe("/onboarding?connect=1"));
    await vi.waitFor(() => expect(screen.getByTestId("status").textContent).toBe("enabled:true"));
  });

  it("browser Back from the app into the demo, and forward again: each side keeps to its own answer", async () => {
    await mountAt("/demo");
    await screen.findByText("enabled:false");
    await act(async () => nav("/onboarding?connect=1"));
    await vi.waitFor(() => expect(screen.getByTestId("status").textContent).toBe("enabled:true"));
    await act(async () => window.history.back());
    await vi.waitFor(() => expect(screen.getByTestId("status").textContent).toBe("enabled:false"));
    await act(async () => window.history.forward());
    await vi.waitFor(() => expect(screen.getByTestId("status").textContent).toBe("enabled:true"));
  });

  it("the app, then the demo: the demo never shows the app's answer", async () => {
    await mountAt("/onboarding?connect=1");
    await screen.findByText("enabled:true");
    await act(async () => nav("/demo"));
    await vi.waitFor(() => expect(screen.getByTestId("status").textContent).toBe("enabled:false"));
  });

  it("no flash: the first render on the other side never shows this side's data", async () => {
    await mountAt("/demo");
    await screen.findByText("enabled:false");
    const seen: string[] = [];
    const obs = new MutationObserver(() => seen.push(screen.getByTestId("status").textContent ?? ""));
    obs.observe(screen.getByTestId("status"), { childList: true, characterData: true, subtree: true });
    await act(async () => nav("/onboarding?connect=1"));
    await vi.waitFor(() => expect(screen.getByTestId("status").textContent).toBe("enabled:true"));
    obs.disconnect();
    expect(seen).not.toContain("enabled:false");
  });
});
