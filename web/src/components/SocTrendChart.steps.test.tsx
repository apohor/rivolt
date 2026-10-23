import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { SocTrendChart, limitSteps } from "./SocTrendChart";

const DAY = 86_400_000;
const OCT = new Date(2026, 9, 1).getTime();
const NOV = new Date(2026, 10, 1).getTime();
const at = (day: number, hour = 0) => OCT + (day - 1) * DAY + hour * 3_600_000;
const charge = (day: number, limit: number | null, hours = 2) => ({ x0: at(day, 8), x1: at(day, 8 + hours), limit });

describe("limitSteps (the iPhone's rule; the chart draws the last step)", () => {
  const now = at(22, 12);

  it("steps at each charge with a limit, carries over unknowns, then the live target to now", () => {
    const steps = limitSteps([charge(3, 80), charge(8, null), charge(12, 90), charge(20, 0)], OCT, NOV, now, 70);
    expect(steps).toEqual([
      { x0: at(3, 8), x1: at(12, 8), pct: 80 },
      { x0: at(12, 8), x1: at(12, 10), pct: 90 },
      { x0: at(12, 10), x1: now, pct: 70 },
    ]);
  });

  it("joins steps at the same value and keeps the last limit when there is no live target", () => {
    expect(limitSteps([charge(3, 80), charge(9, 80)], OCT, NOV, now, null)).toEqual([{ x0: at(3, 8), x1: now, pct: 80 }]);
    expect(limitSteps([charge(5, 100)], OCT, NOV, now, null)).toEqual([{ x0: at(5, 8), x1: now, pct: 100 }]);
    expect(limitSteps([charge(5, null), charge(6, 0)], OCT, NOV, now, 70)).toEqual([]);
  });

  it("a past month ends at its end with its own charges' limits, no live target", () => {
    const sep = new Date(2026, 8, 1).getTime();
    const s = limitSteps([{ x0: sep + 2 * DAY, x1: sep + 2 * DAY + 3_600_000, limit: 80 }], sep, OCT, now, 70);
    expect(s).toEqual([{ x0: sep + 2 * DAY, x1: OCT, pct: 80 }]);
  });
});

// jsdom has no ResizeObserver; the chart only needs it to follow its width.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

describe("SocTrendChart", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });
  // Ordinary days near 60–70%, a road-trip day on the 9th and the 12th.
  const trend = [
    { x: at(2, 13), y: 71 }, { x: at(2, 14), y: 69 },
    { x: at(5, 13), y: 66 }, { x: at(5, 14), y: 62 },
    { x: at(9, 7), y: 97 }, { x: at(9, 14), y: 41 },
    { x: at(12, 9), y: 73 }, { x: at(12, 13), y: 3 }, { x: at(12, 23), y: 70 },
    { x: at(20, 9), y: 80 }, { x: at(20, 10), y: 77 },
  ];
  const charges = [charge(3, 70), charge(8, 100), charge(12, 85), charge(19, 80)];

  it("draws the band and one line, only the two extremes named, one limit, ticks for charging days and a caption", () => {
    vi.useFakeTimers({ now: at(22, 12), toFake: ["Date"] });
    const { container } = render(<SocTrendChart trend={trend} charges={charges} domain={[OCT, NOV]} limit={70} />);
    expect(container.querySelectorAll("path[data-draw='band']")).toHaveLength(1);
    expect(container.querySelectorAll("path[data-draw='line']")).toHaveLength(1);
    expect(container.querySelector("[data-testid='soc-max']")!.textContent).toBe("97% · Oct 9");
    expect(container.querySelector("[data-testid='soc-min']")!.textContent).toBe("3% · Oct 12");
    const limit = container.querySelector("[data-testid='soc-limit']")!;
    expect(limit.textContent).toBe("Limit 70%");
    expect(limit.querySelectorAll("line")).toHaveLength(1);
    expect(container.querySelectorAll("[data-draw='tick']").length).toBeGreaterThan(0);
    // No legend, no bars or strip.
    expect(container.querySelector("[data-soc-legend]")).toBeNull();
    expect(container.querySelector("[data-band]")).toBeNull();
    expect(container.querySelector("[data-soc-caption]")!.textContent).toBe("Band: each day’s range · line: end of day · ticks: charged");
    // One sentence for a screen reader.
    expect(container.querySelector("svg[role='img']")!.getAttribute("aria-label")).toMatch(/^Battery level by day\. \d+ days with readings\. Lowest 3 percent on Oct 12; highest 97 percent on Oct 9\./);
  });

  it("a past month names the last limit known in it, and none when there isn't one", () => {
    vi.useFakeTimers({ now: new Date(2026, 10, 12).getTime(), toFake: ["Date"] });
    const withLimit = render(<SocTrendChart trend={trend} charges={charges} domain={[OCT, NOV]} limit={null} />);
    expect(withLimit.container.querySelector("[data-testid='soc-limit']")!.textContent).toBe("Limit 80%");
    withLimit.unmount();
    const none = render(<SocTrendChart trend={trend} charges={[charge(3, null)]} domain={[OCT, NOV]} limit={null} />);
    expect(none.container.querySelector("[data-testid='soc-limit']")).toBeNull();
  });
});
