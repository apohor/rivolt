import { describe, expect, it } from "vitest";
import { groupByNetwork, safeSourceUrl, type MembershipTerm } from "./membershipTerms";

const row = (slug: string, plan: string): MembershipTerm => ({ network_slug: slug, network: slug.toUpperCase(), plan, terms: "t", source_url: "https://example.com/x", checked: "2026-10-20", label: "As listed." });

describe("membership terms", () => {
  it("groups plans under their network, keeping the server's order", () => {
    const g = groupByNetwork([row("evgo", "Pay As You Go"), row("ea", "Pass+"), row("evgo", "Plus")]);
    expect(g.map((x) => [x.slug, x.plans.map((p) => p.plan)])).toEqual([["evgo", ["Pay As You Go", "Plus"]], ["ea", ["Pass+"]]]);
    expect(groupByNetwork([])).toEqual([]);
  });
  it("links only an https page", () => {
    expect(safeSourceUrl("https://www.evgo.com/pricing/")).toBe("https://www.evgo.com/pricing/");
    expect(safeSourceUrl("http://www.evgo.com/pricing/")).toBeNull();
    expect(safeSourceUrl("javascript:alert(1)")).toBeNull();
    expect(safeSourceUrl("")).toBeNull();
    expect(safeSourceUrl(undefined)).toBeNull();
  });
});
