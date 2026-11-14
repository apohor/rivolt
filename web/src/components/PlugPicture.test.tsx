import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { PlugPicture, plugLabel } from "./PlugPicture";

afterEach(cleanup);

describe("PlugPicture", () => {
  it("plugged in and not charging: a still grey plug badge, no glow, labelled for a screen reader", () => {
    render(<PlugPicture kw={0}><img alt="" /></PlugPicture>);
    expect(screen.getByRole("img", { name: "Plugged in, not charging" })).toBeInTheDocument();
    expect(screen.queryByTestId("plug-glow")).toBeNull();
    expect(screen.getByTestId("plug-picture")).toHaveAttribute("data-plug", "plugged");
  });
  it("charging: a green glow that pulses unless motion is reduced, and a bolt badge with the power", () => {
    render(<PlugPicture kw={11.24}><img alt="" /></PlugPicture>);
    expect(screen.getByRole("img", { name: "Charging, 11.2 kW" })).toBeInTheDocument();
    const glow = screen.getByTestId("plug-glow");
    expect(glow.className).toContain("motion-safe:animate-pulse");
    expect(glow.className).not.toMatch(/(^| )animate-pulse/);
  });
  it("labels", () => {
    expect(plugLabel(null)).toBe("Plugged in, not charging");
    expect(plugLabel(150)).toBe("Charging, 150 kW");
  });
});
