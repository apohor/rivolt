import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { BottomSheet, nearestSnap, snapHeights, stepSnap, type Snap } from "./BottomSheet";

// jsdom has no PointerEvent: a MouseEvent carries clientY, which the sheet reads.
class FakePointerEvent extends MouseEvent {
  pointerId: number;
  pointerType: string;
  constructor(type: string, init: MouseEventInit & { pointerId?: number; pointerType?: string } = {}) {
    super(type, { bubbles: true, cancelable: true, ...init });
    this.pointerId = init.pointerId ?? 1;
    this.pointerType = init.pointerType ?? "touch";
  }
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function mockViewport(wide: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: wide && q.includes("1024"),
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

describe("snap maths", () => {
  it("heights are strictly increasing, also in a short window", () => {
    for (const vh of [844, 600, 320, 200]) {
      const h = snapHeights(vh, 120);
      expect(h.peek).toBeLessThan(h.half);
      expect(h.half).toBeLessThan(h.full);
    }
    expect(snapHeights(844, 120)).toEqual({ peek: 120, half: 422, full: 743 });
  });
  it("a slow release settles on the nearest snap", () => {
    const h = snapHeights(844, 120);
    expect(nearestSnap(150, h, 0)).toBe("peek");
    expect(nearestSnap(300, h, 0.1)).toBe("half");
    expect(nearestSnap(600, h, 0)).toBe("full");
  });
  it("a flick steps one snap from where the drag began", () => {
    const h = snapHeights(844, 120);
    expect(nearestSnap(140, h, 0.9, "peek")).toBe("half");
    expect(nearestSnap(430, h, 0.9, "half")).toBe("full");
    expect(nearestSnap(700, h, -0.9, "full")).toBe("half");
    expect(nearestSnap(430, h, -0.9, "half")).toBe("peek");
    expect(nearestSnap(130, h, -0.9, "peek")).toBe("peek"); // already at the bottom
    expect(nearestSnap(430, h, 0.9)).toBe("full"); // no start given: from the nearest
  });
  it("stepSnap stops at the ends", () => {
    expect(stepSnap("peek", -1)).toBe("peek");
    expect(stepSnap("peek", 1)).toBe("half");
    expect(stepSnap("half", 1)).toBe("full");
    expect(stepSnap("full", 1)).toBe("full");
  });
});

describe("BottomSheet", () => {
  const body = <button>stop details</button>;

  it("is a named region with a button handle; the body is inert at the peek", () => {
    mockViewport(false);
    render(
      <BottomSheet label="Trip details" peek={<p>Summary</p>}>
        {body}
      </BottomSheet>,
    );
    const region = screen.getByRole("region", { name: "Trip details" });
    expect(region).toHaveAttribute("data-snap", "peek");
    const handle = screen.getByRole("button", { name: "Expand Trip details" });
    expect(handle).toHaveAttribute("aria-expanded", "false");
    expect(handle.getAttribute("aria-controls")).toBe(document.querySelector("[data-plan-sheet-body]")!.id);
    expect(screen.getByText("Summary")).toBeVisible();
    expect(document.querySelector("[data-plan-sheet-body]")).toHaveAttribute("inert");
  });

  it("click toggles peek and half; keys step, jump and fold; Escape folds from inside", () => {
    mockViewport(false);
    const seen: Snap[] = [];
    render(
      <BottomSheet label="Trip details" onSnapChange={(s) => seen.push(s)}>
        {body}
      </BottomSheet>,
    );
    const region = screen.getByRole("region", { name: "Trip details" });
    const handle = screen.getByRole("button", { name: /Trip details/ });
    fireEvent.click(handle);
    expect(region).toHaveAttribute("data-snap", "half");
    expect(document.querySelector("[data-plan-sheet-body]")).not.toHaveAttribute("inert");
    expect(screen.getByRole("button", { name: "Collapse Trip details" })).toHaveAttribute("aria-expanded", "true");
    fireEvent.keyDown(handle, { key: "ArrowUp" });
    expect(region).toHaveAttribute("data-snap", "full");
    fireEvent.keyDown(handle, { key: "ArrowDown" });
    expect(region).toHaveAttribute("data-snap", "half");
    fireEvent.keyDown(handle, { key: "End" });
    expect(region).toHaveAttribute("data-snap", "full");
    fireEvent.keyDown(handle, { key: "Home" });
    expect(region).toHaveAttribute("data-snap", "peek");
    fireEvent.keyDown(handle, { key: "End" });
    fireEvent.keyDown(screen.getByText("stop details"), { key: "Escape" });
    expect(region).toHaveAttribute("data-snap", "peek");
    expect(seen).toEqual(["half", "full", "half", "full", "peek", "full", "peek"]);
  });

  it("dragging the handle up settles on a snap and exposes --sheet-h while dragging", () => {
    mockViewport(false);
    vi.stubGlobal("PointerEvent", FakePointerEvent);
    vi.stubGlobal("innerHeight", 844);
    render(<BottomSheet label="Trip details">{body}</BottomSheet>);
    const region = screen.getByRole("region", { name: "Trip details" });
    const handle = screen.getByRole("button", { name: /Trip details/ });
    fireEvent.pointerDown(handle, { clientY: 700, pointerId: 1, button: 0 });
    fireEvent.pointerMove(handle, { clientY: 400, pointerId: 1 });
    expect(region).toHaveAttribute("data-dragging", "true");
    expect(region.style.getPropertyValue("--sheet-h")).toBe("420px"); // 120 + 300 px of drag
    fireEvent.pointerUp(handle, { clientY: 400, pointerId: 1 });
    expect(region).toHaveAttribute("data-snap", "half");
    expect(region).not.toHaveAttribute("data-dragging");
  });

  it("a tap (no movement) is a click, not a drag", () => {
    mockViewport(false);
    vi.stubGlobal("PointerEvent", FakePointerEvent);
    render(<BottomSheet label="Trip details">{body}</BottomSheet>);
    const handle = screen.getByRole("button", { name: /Trip details/ });
    fireEvent.pointerDown(handle, { clientY: 700, pointerId: 1, button: 0 });
    fireEvent.pointerMove(handle, { clientY: 698, pointerId: 1 });
    fireEvent.pointerUp(handle, { clientY: 698, pointerId: 1 });
    expect(screen.getByRole("region", { name: "Trip details" })).toHaveAttribute("data-snap", "peek");
  });

  it("can be controlled", () => {
    mockViewport(false);
    const onSnap = vi.fn();
    const { rerender } = render(
      <BottomSheet label="Trip details" snap="full" onSnapChange={onSnap}>
        {body}
      </BottomSheet>,
    );
    const region = screen.getByRole("region", { name: "Trip details" });
    expect(region).toHaveAttribute("data-snap", "full");
    fireEvent.keyDown(screen.getByRole("button", { name: /Trip details/ }), { key: "Home" });
    expect(onSnap).toHaveBeenCalledWith("peek");
    expect(region).toHaveAttribute("data-snap", "full"); // the parent decides
    rerender(
      <BottomSheet label="Trip details" snap="peek" onSnapChange={onSnap}>
        {body}
      </BottomSheet>,
    );
    expect(region).toHaveAttribute("data-snap", "peek");
  });

  it("at 1024px and wider it is not a sheet: content renders inline", () => {
    mockViewport(true);
    render(
      <BottomSheet label="Trip details" peek={<p>Summary</p>}>
        {body}
      </BottomSheet>,
    );
    expect(screen.queryByRole("region", { name: "Trip details" })).toBeNull();
    expect(document.querySelector("[data-plan-sheet-inline]")).not.toBeNull();
    expect(screen.getByText("Summary")).toBeVisible();
    expect(screen.getByRole("button", { name: "stop details" })).toBeEnabled();
  });
});
