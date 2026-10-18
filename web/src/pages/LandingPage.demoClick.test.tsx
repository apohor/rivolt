import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

const track = vi.fn();
vi.mock("../lib/track", () => ({ track: (...a: unknown[]) => track(...a) }));
vi.mock("../lib/config", () => ({
  getConfig: () => ({ features: { demoEnabled: true, signupEnabled: true } }),
}));

import LandingPage from "./LandingPage";

describe("landing demo links", () => {
  afterEach(() => {
    cleanup();
    track.mockClear();
  });

  it("report demo_click with a fixed where, and nothing else", () => {
    render(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    );
    track.mockClear(); // the landing_view on mount is not the subject here
    fireEvent.click(screen.getByRole("link", { name: "Try the demo truck" }));
    expect(track).toHaveBeenLastCalledWith("demo_click", { where: "hero" });
    fireEvent.click(screen.getByRole("link", { name: "Demo" }));
    expect(track).toHaveBeenLastCalledWith("demo_click", { where: "footer" });
    for (const call of track.mock.calls) {
      expect(call[0]).toBe("demo_click");
      expect(Object.keys(call[1] as object)).toEqual(["where"]);
    }
  });
});
