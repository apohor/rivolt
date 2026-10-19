import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FAR_FUTURE_NOTE, FarFutureNote } from "./FarFutureNote";
import { mergeReplan } from "./replan";
import type { TripPlanMultidayResponse } from "../lib/api";

describe("FarFutureNote", () => {
  it("says the plan is from today's network and to plan again within a month", () => {
    render(<FarFutureNote show />);
    const note = screen.getByRole("note");
    expect(note).toHaveTextContent(/today's network and conditions, not the date you leave/);
    expect(note).toHaveTextContent(/within a month of leaving/);
    expect(note.textContent).toBe(FAR_FUTURE_NOTE);
  });
  it("shows nothing for a near trip", () => {
    const { container } = render(<FarFutureNote show={false} />);
    expect(container).toBeEmptyDOMElement();
    const { container: c2 } = render(<FarFutureNote />);
    expect(c2).toBeEmptyDOMElement();
  });
  it("survives a re-plan of a saved far trip", () => {
    const resp = { days: [], total: { distance_meters: 0, drive_duration_sec: 0, charging_duration_sec: 0 }, far_future: true } as TripPlanMultidayResponse;
    expect(mergeReplan([], 1, resp).far_future).toBe(true);
    expect(mergeReplan([], 1, { ...resp, far_future: undefined }).far_future).toBeUndefined();
  });
});
