import { describe, expect, it } from "vitest";
import { activeTab, buildTabs } from "./tabs";

const base = { tripPlannerEnabled: true, isAdmin: false, liveAvailable: true };

describe("buildTabs", () => {
  it("orders Now · Activity · Money · Plan · Truck", () => {
    expect(buildTabs(base).map((t) => t.label)).toEqual(["Now", "Activity", "Money", "Plan", "Truck"]);
  });

  it("drops Plan when the trip planner flag is off", () => {
    expect(buildTabs({ ...base, tripPlannerEnabled: false }).map((t) => t.key)).toEqual([
      "now",
      "activity",
      "money",
      "truck",
    ]);
  });

  it("adds Admin under Truck only for admins", () => {
    const truck = (isAdmin: boolean) => buildTabs({ ...base, isAdmin }).find((t) => t.key === "truck")!;
    expect(truck(false).subs.map((s) => s.to)).toEqual(["/truck", "/settings"]);
    expect(truck(true).subs.map((s) => s.to)).toEqual(["/truck", "/settings", "/admin"]);
  });

  it("Now and Activity have no sub-nav (one nav row; Live is linked from the Now card)", () => {
    const tabs = buildTabs(base);
    expect(tabs.find((t) => t.key === "now")!.subs).toEqual([]);
    expect(tabs.find((t) => t.key === "activity")!.subs).toEqual([]);
  });
});

describe("activeTab", () => {
  const tabs = buildTabs({ ...base, isAdmin: true });
  it.each([
    ["/", "now"],
    ["/live", "now"],
    ["/drives", "activity"],
    ["/drives/live_x_d_1", "activity"],
    ["/charges/abc", "activity"],
    ["/money", "money"],
    ["/trips/plan", "plan"],
    ["/truck", "truck"],
    ["/settings", "truck"],
    ["/admin", "truck"],
  ])("%s → %s", (path, key) => {
    expect(activeTab(tabs, path)?.key).toBe(key);
  });

  it("matches / exactly and ignores lookalike prefixes", () => {
    expect(activeTab(tabs, "/nope")).toBeUndefined();
    expect(activeTab(tabs, "/moneybags")).toBeUndefined();
  });
});
