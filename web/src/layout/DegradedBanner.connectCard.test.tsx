import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

// signup_v2: "Rivian isn't connected" is not repeated above the connect card; it stays wherever no card is shown.
async function mount(opts: { keys: string[]; rivian?: object; card?: false | "inline" | "page" }) {
  vi.resetModules();
  stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, demo_enabled: false, design_preview: opts.keys } } },
    "/api/auth/me": { body: { id: "u", email: "a@b.c", onboarding_completed: true } },
    "/api/health": { body: { ok: true, status: "ok" } },
    "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false, ...opts.rivian } },
    "/api/events": { body: {} },
  });
  const Banner = (await import("./DegradedBanner")).default;
  const Prompt = (await import("../components/ConnectRivianPrompt")).default;
  const view = (card: typeof opts.card) => (
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <Banner />
        {card && <Prompt inline={card === "inline"} context="Once connected, drives appear here automatically." />}
      </MemoryRouter>
    </QueryClientProvider>
  );
  const r = render(view(opts.card ?? false));
  return { r, view };
}
const banner = () => screen.queryByText("Rivian isn't connected.");

describe("not-connected banner with the connect card", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it.each(["inline", "page"] as const)("key on, card shown (%s: Now, or Drives/Charges/Plan): no banner above it", async (card) => {
    await mount({ keys: ["signup_v2"], card });
    await screen.findByRole("heading", { name: "Connect your Rivian" });
    // Give the banner's own status query time to answer; it must stay away.
    await new Promise((r) => setTimeout(r, 100));
    expect(banner()).toBeNull();
  });

  it("key on, no card on the screen (Money, Truck, Settings): the banner is the warning and stays", async () => {
    await mount({ keys: ["signup_v2"] });
    expect(await screen.findByText("Rivian isn't connected.")).toBeTruthy();
  });

  it("key on: the banner returns when the screen with the card is left", async () => {
    const { r, view } = await mount({ keys: ["signup_v2"], card: "inline" });
    await screen.findByRole("heading", { name: "Connect your Rivian" });
    r.rerender(view(false));
    expect(await screen.findByText("Rivian isn't connected.")).toBeTruthy();
  });

  it("key off: shown with the card, as today", async () => {
    await mount({ keys: [], card: "page" });
    expect(await screen.findByText("Rivian isn't connected.")).toBeTruthy();
    await screen.findByRole("heading", { name: "Connect your Rivian to get started" });
  });

  it.each([
    ["a code is waited for", { mfa_pending: true }, "Rivian is waiting for your verification code."],
    ["the sign-in expired", { needs_reauth: true }, "Your Rivian sign-in expired. Reconnect to keep recording."],
    ["Rivolt paused the connection", { paused: true }, "Rivolt has paused its connection to Rivian. Your history is safe; live updates will resume."],
  ])("key on, card shown, %s: that banner stays", async (_n, rivian, text) => {
    await mount({ keys: ["signup_v2"], card: "inline", rivian });
    expect(await screen.findByText(text)).toBeTruthy();
  });
});
