import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

const cfg = { features: { landingEnabled: true, demoEnabled: false } };
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
    expect(screen.getByRole("link", { name: "Connect my Rivian" })).toHaveAttribute("href", "/login");
    cleanup();
    cfg.features.demoEnabled = true;
    renderPage();
    expect(screen.getByRole("link", { name: "Try the demo truck" })).toHaveAttribute("href", "/demo");
    cfg.features.demoEnabled = false;
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
