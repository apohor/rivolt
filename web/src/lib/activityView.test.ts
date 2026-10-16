import { describe, expect, it } from "vitest";
import { parseActivityView, withActivityView } from "./activityView";

describe("activity view state", () => {
  it("defaults to the feed and ignores unknown values", () => {
    expect(parseActivityView(null)).toBe("feed");
    expect(parseActivityView("feed")).toBe("feed");
    expect(parseActivityView("grid")).toBe("feed");
    expect(parseActivityView("table")).toBe("table");
    expect(parseActivityView("map")).toBe("map");
  });

  it("sets and clears ?view while keeping every other param", () => {
    const base = new URLSearchParams("tab=activity&f=drives");
    expect(withActivityView(base, "map").toString()).toBe("tab=activity&f=drives&view=map");
    expect(withActivityView(new URLSearchParams("tab=activity&view=map"), "table").toString()).toBe("tab=activity&view=table");
    expect(withActivityView(new URLSearchParams("tab=activity&view=map"), "feed").toString()).toBe("tab=activity");
    expect(base.toString()).toBe("tab=activity&f=drives");
  });
});
