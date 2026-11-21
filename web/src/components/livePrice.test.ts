import { describe, expect, it } from "vitest";
import type { LiveSession } from "../lib/api";
import { livePriceText } from "./LivePanel";

const ls = (o: Partial<LiveSession>) => ({ current_price: "", current_currency: "USD", is_free_session: false, ...o }) as LiveSession;

describe("livePriceText", () => {
  it("a zero price that is not free is not priced yet", () => {
    expect(livePriceText(ls({ current_price: "0" }), "dcfc")).toBe("Estimated when it ends");
    expect(livePriceText(ls({ current_price: "0.00" }), "dcfc")).not.toMatch(/0/);
  });
  it("a free session says Free, whatever the price or estimate", () => {
    expect(livePriceText(ls({ current_price: "0", is_free_session: true }), "dcfc")).toBe("Free");
    expect(livePriceText(ls({ is_free_session: true, estimated_cost: 3.4, estimated_currency: "USD" }), "dcfc")).toBe("Free");
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
