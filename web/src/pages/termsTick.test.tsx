import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";
import { rememberTermsTick, takeTermsTick } from "../lib/termsTick";

const V = "2026-10-23";

async function mountSignup(keys: string[]) {
  vi.resetModules();
  stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, design_preview: keys, terms_version: V } } },
    "/api/auth/oidc": { body: [{ name: "google", display_name: "Google", start_url: "/auth/google" }] },
  });
  const { default: Page } = await import("./SignupPage");
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <Page />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("terms tick note", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("is one-shot, per version, and expires", () => {
    rememberTermsTick(V);
    expect(takeTermsTick("2027-01-01")).toBe(false);
    expect(takeTermsTick(V)).toBe(false); // the mismatched read removed it
    rememberTermsTick(V);
    expect(takeTermsTick(V)).toBe(true);
    expect(takeTermsTick(V)).toBe(false);
    vi.useFakeTimers();
    rememberTermsTick(V);
    vi.advanceTimersByTime(16 * 60_000);
    expect(takeTermsTick(V)).toBe(false);
  });

  it("sign-up, key on: box unticked, links beside it, providers and email refused until ticked, note written on ticked click", async () => {
    await mountSignup(["signup_v2"]);
    const box = (await screen.findByRole("checkbox")) as HTMLInputElement;
    expect(box.checked).toBe(false);
    expect(screen.getByRole("link", { name: "Terms of Service" }).getAttribute("href")).toBe("/terms");
    expect(screen.getByRole("link", { name: "Privacy Policy" }).getAttribute("href")).toBe("/privacy");
    expect(screen.queryByText(/by continuing/i)).toBeNull();
    const google = screen.getByRole("button", { name: /Google/ }) as HTMLButtonElement;
    const send = screen.getByRole("button", { name: "Email me a sign-up link" }) as HTMLButtonElement;
    fireEvent.change(screen.getByPlaceholderText("you@example.com"), { target: { value: "a@b.co" } });
    expect(google.disabled).toBe(true);
    expect(send.disabled).toBe(true);
    expect(localStorage.getItem("rivolt.terms_ticked")).toBeNull();
    fireEvent.click(box);
    expect(google.disabled).toBe(false);
    expect(send.disabled).toBe(false);
    expect(localStorage.getItem("rivolt.terms_ticked")).toBeNull(); // ticking alone records nothing
    fireEvent.click(google);
    expect(JSON.parse(localStorage.getItem("rivolt.terms_ticked") ?? "{}").version).toBe(V);
  });

  it("sign-up, key off: no box", async () => {
    await mountSignup([]);
    await screen.findByRole("button", { name: /Google/ });
    expect(screen.queryByRole("checkbox")).toBeNull();
  });
});

describe("TermsGate with a ticked note", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  async function gate(note: boolean) {
    vi.resetModules();
    const calls: string[] = [];
    stubFetchRoutes({
      "/api/terms/accept": { body: { ok: true, terms: { current: V, accepted: V, accepted_at: "x" } } },
      "/api/auth/me": { body: {} },
    });
    const spy = vi.spyOn(globalThis, "fetch");
    if (note) rememberTermsTick(V);
    const { TermsGate } = await import("../components/TermsGate");
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <TermsGate terms={{ current: V, accepted: null, accepted_at: null, required: true }} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    return { calls, spy };
  }

  it("a fresh note for this version is saved once through POST /api/terms/accept with that version", async () => {
    const { spy } = await gate(true);
    await waitFor(() => {
      const post = spy.mock.calls.find(([u]) => String(u).includes("/api/terms/accept"));
      expect(post).toBeTruthy();
      expect(JSON.parse(String((post![1] as RequestInit).body))).toEqual({ version: V });
    });
    expect(localStorage.getItem("rivolt.terms_ticked")).toBeNull();
  });

  it("no note: the One more step screen shows and nothing is posted", async () => {
    const { spy } = await gate(false);
    expect(await screen.findByText("One more step")).toBeTruthy();
    expect(spy.mock.calls.some(([u]) => String(u).includes("/api/terms/accept"))).toBe(false);
  });
});
