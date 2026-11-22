import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

// The finish page (an approved sign-up link, ?token=) and its "What's next" box.
async function mount(keys: string[]) {
  vi.resetModules();
  stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, demo_enabled: false, design_preview: keys } } },
    "/api/auth/oidc": { body: [] },
    "/api/auth/me": { status: 401, body: { error: "no" } },
    "/api/signup/token/t0k3n": { body: { email: "a@b.c" } },
    "/api/events": { body: {} },
  });
  const { default: Page } = await import("./SignupPage");
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={["/signup?token=t0k3n"]}>
        <Page />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("finish page: What's next", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("key on: no 'same login' line and no GitHub walkthrough; a separate driver account is the safer way", async () => {
    await mount(["signup_v2"]);
    const box = await vi.waitFor(() => {
      const el = document.querySelector("[data-signup-next]");
      if (!el) throw new Error("no box");
      return el as HTMLElement;
    });
    expect(box.textContent).toContain("a separate driver account is safer");
    expect(document.body.textContent).not.toMatch(/same login you use/);
    expect(document.querySelector('a[href*="github.com"]')).toBeNull();
    expect(screen.getByRole("link", { name: "how" }).getAttribute("href")).toBe("/driver-account");
  });

  it("key off: today's box, unchanged", async () => {
    await mount([]);
    await screen.findByText(/same login you use in the Rivian/);
    expect(document.querySelector("[data-signup-next]")).toBeNull();
  });
});
