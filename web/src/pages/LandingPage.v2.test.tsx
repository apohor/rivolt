import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

// Design key signup_v2 on: the two doors and the tier wording. Key off is covered by LandingPage.test.tsx.
const cfg = { features: { landingEnabled: true, demoEnabled: true, signupEnabled: true }, designPreview: ["signup_v2"] };
vi.mock("../lib/config", () => ({ getConfig: () => cfg }));

import LandingPage from "./LandingPage";

const renderPage = () =>
  render(
    <MemoryRouter>
      <LandingPage />
    </MemoryRouter>,
  );

describe("LandingPage with signup_v2", () => {
  afterEach(() => cleanup());

  it("offers Add my truck (to sign-up) and Try a sample truck (the demo), hero and footer", () => {
    renderPage();
    const add = screen.getAllByRole("link", { name: "Add my truck" });
    expect(add).toHaveLength(2);
    for (const a of add) expect(a).toHaveAttribute("href", "/signup");
    for (const a of screen.getAllByRole("link", { name: "Try a sample truck" })) expect(a).toHaveAttribute("href", "/demo");
    expect(screen.queryByRole("link", { name: /Connect your Rivian|Plan a sample trip|Try the demo/ })).toBeNull();
  });

  it("keeps the unofficial line and the safe-to-connect link beside the buttons", () => {
    renderPage();
    expect(screen.getAllByText(/unofficial and not affiliated with Rivian/)[0]).toBeTruthy();
    expect(screen.getAllByRole("link", { name: "Is it safe to connect?" })[0]).toHaveAttribute("href", "/safe-to-connect");
  });

  it("names the tiers 'free tier and Pro tier' and adds no banned words", () => {
    const { container } = renderPage();
    expect(screen.getByRole("heading", { name: "Free tier and Pro tier" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Free tier" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Pro tier: $3.99 a month or $34.99 a year" })).toBeTruthy();
    const text = container.textContent ?? "";
    expect(text).not.toMatch(/beta|forever|early access|Rivolt Pro:|Free and Pro/i);
  });

  it("without the demo, shows only Add my truck", () => {
    cfg.features.demoEnabled = false;
    try {
      renderPage();
      expect(screen.queryByRole("link", { name: "Try a sample truck" })).toBeNull();
      expect(screen.getAllByRole("link", { name: "Add my truck" }).length).toBeGreaterThan(0);
    } finally {
      cfg.features.demoEnabled = true;
    }
  });

  it("closed sign-ups: the primary door is Sign in", () => {
    cfg.features.signupEnabled = false;
    try {
      renderPage();
      expect(screen.queryByRole("link", { name: "Add my truck" })).toBeNull();
    } finally {
      cfg.features.signupEnabled = true;
    }
  });
});
