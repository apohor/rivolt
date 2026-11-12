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

function onHost(hostname: string, fn: () => void) {
  const orig = window.location;
  Object.defineProperty(window, "location", { value: { ...orig, hostname, search: "" }, configurable: true });
  try {
    fn();
  } finally {
    Object.defineProperty(window, "location", { value: orig, configurable: true });
  }
}

describe("LandingPage", () => {
  afterEach(() => cleanup());

  it("leads with the trip planner", () => {
    renderPage();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Plan the trip. Charging included.");
    expect(screen.getByRole("heading", { name: "The route, the stops, the bill." })).toBeTruthy();
  });

  it("hides the demo links on a self-host without the demo, and always offers connect", () => {
    renderPage();
    expect(screen.queryByRole("link", { name: /demo|sample trip/i })).toBeNull();
    expect(screen.getAllByRole("link", { name: "Connect your Rivian" })[0]).toHaveAttribute("href", "/signup");
  });

  it("opens the in-app demo planner when the install has the demo on", () => {
    cfg.features.demoEnabled = true;
    try {
      renderPage();
      for (const a of screen.getAllByRole("link", { name: "Plan a sample trip" })) expect(a).toHaveAttribute("href", "/demo/trips/plan");
      expect(screen.getByRole("link", { name: "Try the demo" })).toHaveAttribute("href", "/demo");
    } finally {
      cfg.features.demoEnabled = false;
    }
  });

  it("links the public demo host on rivolt.dev", () => {
    onHost("rivolt.dev", () => {
      renderPage();
      expect(screen.getAllByRole("link", { name: "Plan a sample trip" })[0]).toHaveAttribute("href", "https://demo.rivolt.dev/demo/trips/plan");
      expect(screen.getByRole("link", { name: "Try the demo" })).toHaveAttribute("href", "https://demo.rivolt.dev/demo");
      expect(screen.getByText(/ZeroDowntime LLC/)).toBeTruthy();
    });
  });

  it("points connect at /login when signup is closed", () => {
    cfg.features.signupEnabled = false;
    try {
      renderPage();
      expect(screen.queryByRole("link", { name: "Connect your Rivian" })).toBeNull();
      for (const a of screen.getAllByRole("link", { name: "Sign in" })) expect(a).toHaveAttribute("href", "/login");
    } finally {
      cfg.features.signupEnabled = true;
    }
  });

  it("keeps every footer link", () => {
    renderPage();
    const want: [string, string][] = [
      ["GitHub", "https://github.com/apohor/rivolt"],
      ["Privacy", "/privacy"],
      ["Terms", "/terms"],
      ["Support", "/support"],
      ["Is it safe to connect?", "/safe-to-connect"],
      ["Moving from ElectraFi", "/moving-from-electrafi"],
      ["Lease calculator", "/lease-calculator"],
    ];
    for (const [name, href] of want) expect(screen.getByRole("link", { name })).toHaveAttribute("href", href);
  });

  it("shows the sample trip at rest, labelled as a sample", () => {
    renderPage();
    expect(screen.getByText("Sample trip")).toBeTruthy();
    expect(screen.getByText("Arrives 12:04 PM after 264 miles with 30% battery; charging $13.64.")).toBeTruthy();
  });

  it("stays inside the allowed claims", () => {
    const { container } = renderPage();
    const text = container.textContent ?? "";
    expect(text).not.toMatch(/real-?time|battery-friendly/i);
    expect(text).not.toMatch(/rivian'?s app (doesn|does not|shows|has no)/i);
    expect(text).not.toMatch(/free|beta|trial|\/mo\b|per month/i);
    expect(text).toMatch(/not affiliated with, endorsed by or sponsored by Rivian/);
    // The only dollar figure is the sample trip's charging cost.
    expect(text.match(/\$\d+(\.\d+)?/g)?.every((m) => m === "$13.64")).toBe(true);
  });

  it("loads nothing third-party", () => {
    const { container } = renderPage();
    for (const el of container.querySelectorAll("[src],link[href],script")) {
      expect(el.getAttribute("src") ?? "").not.toMatch(/^https?:/);
    }
  });
});
