import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { SocTrendChart, limitSteps } from "./SocTrendChart";

const DAY = 86_400_000;
const OCT = new Date(2026, 9, 1).getTime();
const NOV = new Date(2026, 10, 1).getTime();
const at = (day: number, hour = 0) => OCT + (day - 1) * DAY + hour * 3_600_000;
const charge = (day: number, limit: number | null, hours = 2) => ({ x0: at(day, 8), x1: at(day, 8 + hours), limit });

describe("limitSteps (the iPhone's rule)", () => {
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
    expect(limitSteps([charge(3, 80), charge(9, 80)], OCT, NOV, now, 80)).toEqual([{ x0: at(3, 8), x1: now, pct: 80 }]);
  });

  it("draws a 100% limit, nothing before the first charge with one, and nothing when none has one", () => {
    expect(limitSteps([charge(5, 100)], OCT, NOV, now, null)).toEqual([{ x0: at(5, 8), x1: now, pct: 100 }]);
    expect(limitSteps([charge(5, null), charge(6, 0)], OCT, NOV, now, 70)).toEqual([]);
    expect(limitSteps([charge(5, 101)], OCT, NOV, now, 70)).toEqual([]);
  });

  it("a past month ends at its end with its own charges' limits, no live target", () => {
    const sep = new Date(2026, 8, 1).getTime();
    const s = limitSteps([{ x0: sep + 2 * DAY, x1: sep + 2 * DAY + 3_600_000, limit: 80 }], sep, OCT, now, 70);
    expect(s).toEqual([{ x0: sep + 2 * DAY, x1: OCT, pct: 80 }]);
  });

  it("ignores charges outside the period", () => {
    expect(limitSteps([{ x0: OCT - DAY, x1: OCT - DAY + 3_600_000, limit: 60 }, charge(10, 80)], OCT, NOV, now, null)).toEqual([
      { x0: at(10, 8), x1: now, pct: 80 },
    ]);
  });
});

// jsdom has no ResizeObserver; the chart only needs it to follow its width.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

describe("SocTrendChart limit", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });
  const trend = [
    { x: at(2), y: 60 },
    { x: at(12), y: 85 },
    { x: at(22, 11), y: 70 },
  ];

  it("draws the steps with one value at now and the legend 'Charge limit'", () => {
    vi.useFakeTimers({ now: at(22, 12), toFake: ["Date"] });
    const { container } = render(
      <SocTrendChart trend={trend} charges={[charge(3, 80), charge(12, 90)]} drives={[]} domain={[OCT, NOV]} limit={70} />,
    );
    const g = container.querySelector("[data-testid='soc-limit']")!;
    expect(g.getAttribute("data-steps")).toBe("3");
    expect(g.querySelector("path[data-limit-line]")!.getAttribute("d")).toMatch(/^M [\d.]+ [\d.]+ H [\d.]+ V [\d.]+ H [\d.]+ V [\d.]+ H [\d.]+$/);
    expect(g.querySelectorAll("text")).toHaveLength(1);
    expect(g.textContent).toBe("Limit 70%");
    expect(container.querySelector("[data-legend='limit']")!.textContent).toBe("Charge limit");
  });

  it("without stored limits keeps today's two-day segment, legend 'Charge limit now'", () => {
    vi.useFakeTimers({ now: at(22, 12), toFake: ["Date"] });
    const { container } = render(<SocTrendChart trend={trend} charges={[charge(3, null)]} drives={[]} domain={[OCT, NOV]} limit={70} />);
    const g = container.querySelector("[data-testid='soc-limit']")!;
    expect(g.getAttribute("data-steps")).toBe("0");
    expect(g.textContent).toBe("Limit 70%");
    expect(container.querySelector("[data-legend='limit']")!.textContent).toBe("Charge limit now");
  });

  it("a past month shows its steps without a value", () => {
    vi.useFakeTimers({ now: at(22, 12), toFake: ["Date"] });
    const sep = new Date(2026, 8, 1).getTime();
    const { container } = render(
      <SocTrendChart
        trend={[{ x: sep + DAY, y: 50 }, { x: sep + 10 * DAY, y: 80 }]}
        charges={[{ x0: sep + 2 * DAY, x1: sep + 2 * DAY + 3_600_000, limit: 80 }]}
        drives={[]}
        domain={[sep, OCT]}
        limit={70}
      />,
    );
    const g = container.querySelector("[data-testid='soc-limit']")!;
    expect(g.getAttribute("data-steps")).toBe("1");
    expect(g.querySelectorAll("text")).toHaveLength(0);
  });
});
