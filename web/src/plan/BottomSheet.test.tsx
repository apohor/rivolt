import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { BottomSheet, dragMovesSheet, settleSnap, snapHeights, stepSnap, type Snap } from "./BottomSheet";

// jsdom has no PointerEvent: a MouseEvent carries clientX/Y, which the sheet reads. Touch has its own listeners, so these are a mouse.
class FakePointerEvent extends MouseEvent {
  pointerId: number;
  pointerType: string;
  constructor(type: string, init: MouseEventInit & { pointerId?: number; pointerType?: string } = {}) {
    super(type, { bubbles: true, cancelable: true, ...init });
    this.pointerId = init.pointerId ?? 1;
    this.pointerType = init.pointerType ?? "mouse";
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
  it("the peek is what its text needs, up to 35 % of the room; the heights stay strictly increasing", () => {
    expect(snapHeights(743, 90)).toEqual({ peek: 90, half: 342, full: 743 });
    expect(snapHeights(743, 400).peek).toBe(260); // capped
    for (const room of [743, 500, 300, 160]) {
      const h = snapHeights(room, 120);
      expect(h.peek).toBeLessThan(h.half);
      expect(h.half).toBeLessThan(h.full);
    }
  });

  const h = snapHeights(743, 150); // peek 150, half 342, full 743

  it("a medium flick from the peek opens half, not the details", () => {
    expect(settleSnap("peek", 40, 0.8, h)).toBe("half");
    expect(settleSnap("peek", 150, 0.8, h)).toBe("half");
    expect(settleSnap("peek", 40, 0.1, h)).toBe("peek"); // slow and short: stays
  });

  it("a drag past half goes on to full once it is a third of the way there", () => {
    expect(settleSnap("peek", 350, 0.1, h)).toBe("full");
    expect(settleSnap("peek", 250, 0.1, h)).toBe("half");
    expect(settleSnap("half", 150, 0, h)).toBe("full");
    expect(settleSnap("half", 100, 0, h)).toBe("half");
  });

  it("down from full: a short drag stays, a longer one or a flick steps to half", () => {
    expect(settleSnap("full", -100, 0, h)).toBe("full");
    expect(settleSnap("full", -200, 0, h)).toBe("half");
    expect(settleSnap("full", -20, -0.8, h)).toBe("half");
    expect(settleSnap("full", -600, -0.2, h)).toBe("peek");
    expect(settleSnap("peek", -30, -0.9, h)).toBe("peek"); // already at the bottom
    expect(settleSnap("half", 0, 0, h)).toBe("half");
  });

  it("at full a drag in the details moves the sheet only downwards with them at their top", () => {
    expect(dragMovesSheet("full", true, true, -20)).toBe(true);
    expect(dragMovesSheet("full", true, true, 20)).toBe(false);
    expect(dragMovesSheet("full", true, false, -20)).toBe(false);
    expect(dragMovesSheet("full", false, false, 20)).toBe(true); // on the handle or the title
    expect(dragMovesSheet("half", true, false, 20)).toBe(true);
    expect(dragMovesSheet("peek", true, true, -20)).toBe(true);
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

  it("is a named region with a button handle that says how open it is; the body is inert at the peek", () => {
    mockViewport(false);
    render(
      <BottomSheet label="Trip details" peek={<p>Summary</p>}>
        {body}
      </BottomSheet>,
    );
    const region = screen.getByRole("region", { name: "Trip details" });
    expect(region).toHaveAttribute("data-snap", "peek");
    const handle = screen.getByRole("button", { name: "Trip details: collapsed, expand" });
    expect(handle).toHaveAttribute("aria-expanded", "false");
    expect(handle.getAttribute("aria-controls")).toBe(document.querySelector("[data-plan-sheet-body]")!.id);
    expect(screen.getByText("Summary")).toBeVisible();
    expect(document.querySelector("[data-plan-sheet-body]")).toHaveAttribute("inert");
    expect(screen.queryByRole("button", { name: "Map" })).toBeNull();
  });

  it("click cycles peek, half, full and back; keys step, jump and fold; Escape folds from inside", () => {
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
    expect(screen.getByRole("button", { name: "Trip details: half open, expand fully" })).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(handle);
    expect(region).toHaveAttribute("data-snap", "full");
    expect(screen.getByRole("button", { name: "Trip details: fully open, collapse" })).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(handle);
    expect(region).toHaveAttribute("data-snap", "peek");
    fireEvent.click(handle);
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
    expect(seen).toEqual(["half", "full", "peek", "half", "full", "half", "full", "peek", "full", "peek"]);
  });

  it("at full the focus moves into the details, at the peek back to the handle", () => {
    mockViewport(false);
    render(<BottomSheet label="Trip details">{body}</BottomSheet>);
    const handle = screen.getByRole("button", { name: /Trip details/ });
    fireEvent.keyDown(handle, { key: "End" });
    expect(document.activeElement).toBe(screen.getByRole("group", { name: "Trip details, details" }));
    fireEvent.keyDown(screen.getByText("stop details"), { key: "Escape" });
    expect(document.activeElement).toBe(handle);
  });

  it("a tap on the peek opens the details, the Map button shows the map; controls in the peek keep their taps", () => {
    mockViewport(false);
    render(
      <BottomSheet label="Trip details" peek={<div><p>Summary text</p><button type="button">Route 2</button></div>}>
        {body}
      </BottomSheet>,
    );
    const region = screen.getByRole("region", { name: "Trip details" });
    fireEvent.click(screen.getByRole("button", { name: "Route 2" }));
    expect(region).toHaveAttribute("data-snap", "peek");
    for (let i = 0; i < 5; i++) {
      fireEvent.click(screen.getByText("Summary text"));
      expect(region).toHaveAttribute("data-snap", "full");
      fireEvent.click(screen.getByText("Summary text")); // open: the summary does not fold it
      expect(region).toHaveAttribute("data-snap", "full");
      fireEvent.click(screen.getByRole("button", { name: "Map" }));
      expect(region).toHaveAttribute("data-snap", "peek");
    }
  });

  it("an upright drag past half settles at full; it exposes --sheet-h while it moves, and the click it ends in is not a tap", () => {
    mockViewport(false);
    vi.stubGlobal("PointerEvent", FakePointerEvent);
    vi.stubGlobal("innerHeight", 844); // room 743 in jsdom: peek 150, half 342, full 743
    render(<BottomSheet label="Trip details" peek={<p>Summary</p>}>{body}</BottomSheet>);
    const region = screen.getByRole("region", { name: "Trip details" });
    const title = screen.getByText("Summary");
    fireEvent.pointerDown(title, { clientX: 100, clientY: 700, button: 0 });
    fireEvent.pointerMove(title, { clientX: 101, clientY: 600 });
    expect(region).toHaveAttribute("data-dragging", "true");
    expect(region.style.getPropertyValue("--sheet-h")).toBe("250px"); // 150 + 100 px of drag
    fireEvent.pointerMove(title, { clientX: 101, clientY: 100 });
    fireEvent.pointerUp(title, { clientX: 101, clientY: 100 });
    fireEvent.click(title);
    expect(region).toHaveAttribute("data-snap", "full");
    expect(region).not.toHaveAttribute("data-dragging");
  });

  it("a sideways drag and a tap within the slop leave the sheet where it is", () => {
    mockViewport(false);
    vi.stubGlobal("PointerEvent", FakePointerEvent);
    render(<BottomSheet label="Trip details">{body}</BottomSheet>);
    const region = screen.getByRole("region", { name: "Trip details" });
    const handle = screen.getByRole("button", { name: /Trip details/ });
    fireEvent.pointerDown(handle, { clientX: 100, clientY: 700, button: 0 });
    fireEvent.pointerMove(handle, { clientX: 220, clientY: 690 });
    expect(region).not.toHaveAttribute("data-dragging");
    fireEvent.pointerUp(handle, { clientX: 220, clientY: 690 });
    expect(region).toHaveAttribute("data-snap", "peek");
    fireEvent.pointerDown(handle, { clientX: 100, clientY: 700, button: 0 });
    fireEvent.pointerMove(handle, { clientX: 100, clientY: 694 });
    fireEvent.pointerUp(handle, { clientX: 100, clientY: 694 });
    expect(region).not.toHaveAttribute("data-dragging");
    expect(region).toHaveAttribute("data-snap", "peek");
  });

  it("a resize keeps the height it is at", () => {
    mockViewport(false);
    vi.stubGlobal("innerHeight", 844);
    render(<BottomSheet label="Trip details" defaultSnap="half">{body}</BottomSheet>);
    const region = screen.getByRole("region", { name: "Trip details" });
    act(() => {
      vi.stubGlobal("innerHeight", 600);
      window.dispatchEvent(new Event("resize"));
    });
    expect(region).toHaveAttribute("data-snap", "half");
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

  it("at 1024px and wider it is not a sheet: the details render inline, without the peek", () => {
    mockViewport(true);
    render(
      <BottomSheet label="Trip details" peek={<p>Summary</p>}>
        {body}
      </BottomSheet>,
    );
    expect(screen.queryByRole("region", { name: "Trip details" })).toBeNull();
    expect(document.querySelector("[data-plan-sheet-inline]")).not.toBeNull();
    expect(screen.queryByText("Summary")).toBeNull();
    expect(screen.getByRole("button", { name: "stop details" })).toBeEnabled();
  });
});
