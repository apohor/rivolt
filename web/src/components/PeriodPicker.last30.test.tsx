import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { PeriodPicker } from "./PeriodPicker";
import { LAST_30 } from "../lib/period";

describe("the period picker's 30 days choice", () => {
  afterEach(cleanup);

  it("sits between Month and Custom, is pressed when chosen, and picks the rolling period", () => {
    const onChange = vi.fn();
    const { rerender } = render(<PeriodPicker value={{ kind: "month", offset: 0 }} onChange={onChange} />);
    const names = screen.getAllByRole("button", { pressed: false }).map((b) => b.textContent);
    expect(names).toEqual(expect.arrayContaining(["Day", "Week", "30 days", "Custom"]));
    expect(names.indexOf("30 days")).toBeGreaterThan(names.indexOf("Week"));
    expect(names.indexOf("30 days")).toBeLessThan(names.indexOf("Custom"));
    fireEvent.click(screen.getAllByRole("button", { name: "30 days" })[0]);
    expect(onChange).toHaveBeenCalledWith({ kind: "last30", offset: 0 });
    rerender(<PeriodPicker value={LAST_30} onChange={onChange} />);
    expect(screen.getAllByRole("button", { name: "30 days" })[0]).toHaveAttribute("aria-pressed", "true");
  });
});
