import { describe, expect, it } from "vitest";
import type { LiveSession } from "../lib/api";
import { livePriceText } from "./LivePanel";

const ls = (o: Partial<LiveSession>) => ({ current_price: "", current_currency: "USD", is_free_session: false, ...o }) as LiveSession;

describe("livePriceText", () => {
  it("a zero price that is not free is not priced yet", () => {
    expect(livePriceText(ls({ current_price: "0" }), "dcfc")).toBe("Estimated when it ends");
    expect(livePriceText(ls({ current_price: "0.00" }), "dcfc")).not.toMatch(/0/);
  });
  it("an active Rivian-network session that is free says Free, whatever the price or estimate", () => {
    const free = { active: true, is_free_session: true, is_rivian_charger: true };
    expect(livePriceText(ls({ ...free, current_price: "0" }), "dcfc")).toBe("Free");
    expect(livePriceText(ls({ ...free, estimated_cost: 3.4, estimated_currency: "USD" }), "home_l2")).toBe("Free");
  });
  it("a fast charge or AC away that Rivian calls free is free even when the charger flag is absent", () => {
    expect(livePriceText(ls({ active: true, is_free_session: true }), "dcfc")).toBe("Free");
    expect(livePriceText(ls({ active: true, is_free_session: true }), "away_ac")).toBe("Free");
  });
  it("a home charge Rivian marks free is not free: no Free, the estimate shows as the cost", () => {
    const home = { active: true, is_free_session: true, current_price: "0" };
    expect(livePriceText(ls(home), "home_l2")).toBeNull();
    expect(livePriceText(ls({ ...home, estimated_cost: 1.02, estimated_currency: "USD" }), "home_l2")).toBe("1.02 USD estimate · home rate");
    expect(livePriceText(ls({ ...home, estimated_cost: 1.02, estimated_currency: "USD" }), null)).not.toBe("Free");
  });
  it("an ended session is not this charge's, so it is never free", () => {
    expect(livePriceText(ls({ active: false, is_free_session: true, is_rivian_charger: true }), "dcfc")).toBe("Estimated when it ends");
  });
  it("no price (empty or absent session) is not priced yet", () => {
    expect(livePriceText(ls({}), "dcfc")).toBe("Estimated when it ends");
    expect(livePriceText(null, "dcfc")).toBe("Estimated when it ends");
  });
  it("a real price is shown as it came", () => {
    expect(livePriceText(ls({ current_price: "4.20" }), "dcfc")).toBe("4.20 USD");
  });
  it("an estimate is named as one, with its source, and carries no approximate sign", () => {
    const t = livePriceText(ls({ current_price: "0", estimated_cost: 3.4, estimated_currency: "USD" }), "dcfc");
    expect(t).toBe("3.40 USD estimate · home rate");
    expect(t).not.toMatch(/[~≈]/);
  });
  it("with nothing to show it says what the Now card says for that charge: AC with no home rate shows no cell", () => {
    expect(livePriceText(ls({}), "away_ac")).toBe("Unknown, add it after");
    expect(livePriceText(ls({}), "home_l2")).toBeNull();
    expect(livePriceText(ls({}), null)).toBeNull();
    expect(livePriceText(null, "home_l2")).toBeNull();
  });
});
