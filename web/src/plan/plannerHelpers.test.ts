import { describe, expect, it } from "vitest";
import { ApiError } from "../lib/api";
import { planErrorCopy } from "./plannerHelpers";

describe("planErrorCopy", () => {
  it("names the wait on a quota error", () => {
    const c = planErrorCopy(new ApiError(429, { error: "slow down", class: "plan_quota", retry_after_sec: 600 }));
    expect(c.title).toBe("You've planned a lot in the last hour.");
    expect(c.detail).toContain("about 10 minutes");
  });
  it("says what is missing when the truck's charge is unknown", () => {
    expect(planErrorCopy(new ApiError(400, { error: "vehicle, starting SoC, and pack capacity required" })).title).toBe(
      "We don't know your truck's current charge.",
    );
  });
  it("tells too many days from too many stops", () => {
    expect(planErrorCopy(new ApiError(400, { error: "at most 10 days" })).title).toBe("Trips are limited to 10 days.");
    expect(planErrorCopy(new ApiError(400, { error: "day 2 has more than 10 waypoints" })).detail).toContain("remove a via");
  });
  it("blames the network, not the trip, when the request never arrived", () => {
    expect(planErrorCopy(new TypeError("Failed to fetch")).title).toBe("We couldn't reach Rivolt.");
  });
  it("falls back to the planner being unavailable", () => {
    expect(planErrorCopy(new ApiError(502, "bad gateway")).title).toBe("Rivian's planner isn't answering right now.");
  });
});
