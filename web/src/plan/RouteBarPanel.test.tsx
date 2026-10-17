import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { RouteBar } from "./RouteBarPanel";
import { countNonDefaultOptions } from "./routeBar";

afterEach(cleanup);

function setup(over: Partial<{ value: string; live: number | null; moreCount: number }> = {}) {
  const onSwap = vi.fn();
  const onSoc = vi.fn();
  render(
    <RouteBar
      from={<input aria-label="From field" />}
      to={<input aria-label="To field" />}
      onSwap={onSwap}
      when={<button type="button">Now</button>}
      soc={{ value: over.value ?? "", live: over.live === undefined ? 62 : over.live, onChange: onSoc }}
      moreCount={over.moreCount ?? 0}
      more={<button type="button">Target arrival SoC</button>}
      submit={<div data-plan-submit><button type="submit">Plan trip</button></div>}
    />,
  );
  return { onSwap, onSoc };
}

describe("RouteBar", () => {
  it("shows the always-visible parts and keeps More options closed", () => {
    setup();
    expect(screen.getByLabelText("From field")).toBeVisible();
    expect(screen.getByLabelText("To field")).toBeVisible();
    expect(screen.getByRole("button", { name: "Now" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Plan trip" })).toBeVisible();
    const toggle = screen.getByRole("button", { name: /More options/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(toggle.getAttribute("aria-controls")).toBe(document.querySelector("[data-plan-more-body]")!.id);
    expect(document.querySelector("[data-plan-more-body]")).toHaveAttribute("hidden");
    // Hidden content is not reachable by role queries.
    expect(screen.queryByRole("button", { name: "Target arrival SoC" })).toBeNull();
  });

  it("the disclosure opens and closes, and the badge counts changed options", () => {
    setup({ moreCount: 3 });
    const toggle = screen.getByRole("button", { name: /More options/ });
    expect(toggle).toHaveTextContent("3");
    expect(screen.getByLabelText("3 changed")).toBeInTheDocument();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: "Target arrival SoC" })).toBeVisible();
    fireEvent.click(toggle);
    expect(screen.queryByRole("button", { name: "Target arrival SoC" })).toBeNull();
  });

  it("no badge when everything is at its default", () => {
    setup({ moreCount: 0 });
    expect(document.querySelector("[data-plan-more-count]")).toBeNull();
  });

  it("swap is a labelled button between From and To", () => {
    const { onSwap } = setup();
    const swap = screen.getByRole("button", { name: "Swap From and To" });
    const nodes = [...document.querySelector("[data-plan-endpoints]")!.children];
    expect(nodes.indexOf(swap)).toBe(1); // From, swap, To
    fireEvent.click(swap);
    expect(onSwap).toHaveBeenCalledTimes(1);
  });

  it("the SoC slider shows the live value when empty and a typed value with a reset", () => {
    const { onSoc } = setup({ live: 62 });
    const slider = screen.getByRole("slider", { name: "Starting SoC" });
    expect(slider).toHaveValue("62");
    expect(slider).toHaveAttribute("aria-valuetext", "62 percent, live");
    expect(document.querySelector("[data-plan-soc] output")).toHaveTextContent("62% (live)");
    expect(screen.queryByRole("button", { name: "Use live" })).toBeNull();
    fireEvent.change(slider, { target: { value: "80" } });
    expect(onSoc).toHaveBeenCalledWith("80");
  });

  it("a typed SoC shows with a 'Use live' reset that clears it", () => {
    cleanup();
    const { onSoc } = setup({ value: "85", live: 62 });
    expect(screen.getByRole("slider", { name: "Starting SoC" })).toHaveValue("85");
    expect(document.querySelector("[data-plan-soc] output")).toHaveTextContent("85%");
    expect(document.querySelector("[data-plan-soc] output")).not.toHaveTextContent("live");
    fireEvent.click(screen.getByRole("button", { name: "Use live" }));
    expect(onSoc).toHaveBeenCalledWith("");
  });

  it("with no typed value and no live state it reads auto and the slider rests at 80", () => {
    setup({ live: null });
    expect(document.querySelector("[data-plan-soc] output")).toHaveTextContent("auto");
    expect(screen.getByRole("slider", { name: "Starting SoC" })).toHaveValue("80");
  });
});

describe("countNonDefaultOptions", () => {
  const base = { viaStops: 0, targetSoc: "20", driveModeIsDefault: true, hasAdapter: false };
  it("is zero at the defaults, including an empty target", () => {
    expect(countNonDefaultOptions(base)).toBe(0);
    expect(countNonDefaultOptions({ ...base, targetSoc: " " })).toBe(0);
  });
  it("counts each changed option once", () => {
    expect(countNonDefaultOptions({ ...base, viaStops: 2 })).toBe(2);
    expect(countNonDefaultOptions({ ...base, targetSoc: "10" })).toBe(1);
    expect(countNonDefaultOptions({ ...base, driveModeIsDefault: false })).toBe(1);
    expect(countNonDefaultOptions({ ...base, hasAdapter: true })).toBe(1);
    expect(countNonDefaultOptions({ viaStops: 1, targetSoc: "15", driveModeIsDefault: false, hasAdapter: true })).toBe(4);
  });
});
