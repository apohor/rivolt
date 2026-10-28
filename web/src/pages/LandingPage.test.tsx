import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

const cfg = { features: { landingEnabled: true, demoEnabled: false, signupEnabled: true } };
vi.mock("../lib/config", () => ({ getConfig: () => cfg }));

import LandingPage from "./LandingPage";

function renderPage() {
  return render(
    <MemoryRouter>
      <LandingPage />
    </MemoryRouter>,
  );
}

describe("LandingPage", () => {
  afterEach(() => cleanup());

  it("hides the demo CTA until demo_enabled, and always offers connect", () => {
    renderPage();
    expect(screen.queryByRole("link", { name: /demo/i })).toBeNull();
    expect(screen.getByRole("link", { name: "Create an account" })).toHaveAttribute("href", "/signup");
    cleanup();
    cfg.features.demoEnabled = true;
    renderPage();
    expect(screen.getByRole("link", { name: "Try the demo truck" })).toHaveAttribute("href", "/demo");
    cfg.features.demoEnabled = false;
  });

  it("links a planned sample trip under the trip claim, not the demo host", () => {
    renderPage();
    expect(screen.getByRole("link", { name: "See a planned trip first: Los Angeles to Las Vegas in two days" })).toHaveAttribute(
      "href",
      "/sample-trips/la-las-vegas-two-days",
    );
  });

  it("points sign-up at /login when signup is closed", () => {
    cfg.features.signupEnabled = false;
    renderPage();
    expect(screen.getAllByRole("link", { name: "Sign in" }).length).toBe(2);
    expect(screen.queryByRole("link", { name: "Create an account" })).toBeNull();
    cfg.features.signupEnabled = true;
  });

  it("links the public demo host on rivolt.dev", () => {
    const orig = window.location;
    Object.defineProperty(window, "location", { value: { ...orig, hostname: "rivolt.dev", search: "" }, configurable: true });
    try {
      renderPage();
      expect(screen.getByRole("link", { name: "Try the demo truck" })).toHaveAttribute("href", "https://demo.rivolt.dev");
    } finally {
      Object.defineProperty(window, "location", { value: orig, configurable: true });
    }
  });

  it("explains the ElectraFi import and links the steps", () => {
    renderPage();
    expect(screen.getByRole("heading", { name: "Coming from ElectraFi?" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Import steps" })).toHaveAttribute("href", "/moving-from-electrafi");
  });

  it("links the connect-safety page", () => {
    renderPage();
    expect(screen.getByRole("link", { name: "Is it safe to connect my Rivian account?" })).toHaveAttribute("href", "/safe-to-connect");
  });

  it("links Privacy and Terms", () => {
    renderPage();
    expect(screen.getByRole("link", { name: "Privacy" })).toHaveAttribute("href", "/privacy");
    expect(screen.getByRole("link", { name: "Terms" })).toHaveAttribute("href", "/terms");
  });

  it("stays inside the allowed claims", () => {
    const { container } = renderPage();
    const text = container.textContent ?? "";
    expect(text).not.toMatch(/real-?time|battery-friendly/i);
    expect(text).not.toMatch(/rivian'?s app (doesn|does not|shows|has no)/i);
    expect(text).toMatch(/not affiliated with Rivian/);
    // Illustration figures are masked, never real dollars.
    expect(text).not.toMatch(/\$\d/);
  });

  it("loads nothing third-party", () => {
    const { container } = renderPage();
    for (const el of container.querySelectorAll("[src],link[href],script")) {
      expect(el.getAttribute("src") ?? "").not.toMatch(/^https?:/);
    }
  });
});
