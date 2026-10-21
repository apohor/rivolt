import { describe, expect, it } from "vitest";
import { groupByNetwork, safeSourceUrl, type MembershipTerm } from "./membershipTerms";

const row = (slug: string, plan: string): MembershipTerm => ({ network_slug: slug, network: slug.toUpperCase(), plan, terms: "t", source_url: "https://example.com/x", checked: "2026-10-20", label: "As listed." });

describe("membership terms", () => {
  it("groups plans under their network, keeping the server's order", () => {
    const g = groupByNetwork([row("evgo", "Pay As You Go"), row("ea", "Pass+"), row("evgo", "Plus")]);
    expect(g.map((x) => [x.slug, x.plans.map((p) => p.plan)])).toEqual([["evgo", ["Pay As You Go", "Plus"]], ["ea", ["Pass+"]]]);
    expect(groupByNetwork([])).toEqual([]);
  });
  it("links only an https page on a network's own site", () => {
    for (const ok of [
      "https://www.evgo.com/pricing/",
      "https://www.electrifyamerica.com/pricing/",
      "https://rivian.com/support/article/how-much-does-it-cost-to-charge-on-the-rivian-adventure-network",
      "https://www.shell.us/electric-vehicle-charging/shell-app-ev-faqs.html",
      "https://www.tesla.com/support/charging/supercharging-other-evs",
    ]) expect(safeSourceUrl(ok)).toBe(ok);
    for (const bad of [
      "http://www.evgo.com/pricing/",
      "javascript:alert(1)",
      "https://evil.example/www.evgo.com/",
      "https://evgo.com.evil.example/pricing",
      "https://notevgo.com/pricing",
      "https://user:pw@www.evgo.com/pricing/",
      "",
    ]) expect(safeSourceUrl(bad), bad).toBeNull();
    expect(safeSourceUrl(undefined)).toBeNull();
  });
});
