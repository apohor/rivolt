import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { ownChargeEtaMins, pickChargeEta } from "./chargeEta";

// Shared with internal/rivian/charge_eta.go and ios/Rivolt/Views/ChargeETA.swift: all three pass every vector.
const file = path.join(__dirname, "../../../internal/rivian/testdata/charge_eta_vectors.json");
const v = JSON.parse(fs.readFileSync(file, "utf8")) as {
  pick: { name: string; truck_mins: number; validity: string; soc_pct: number; limit_pct: number; ours_mins: number; want_mins: number; want_source: string }[];
  own: { name: string; soc_pct: number; limit_pct: number; pack_kwh: number; power_kw: number; want_mins: number }[];
};

describe("shared charge ETA vectors", () => {
  it("has the vectors", () => {
    expect(v.pick.length).toBeGreaterThanOrEqual(15);
    expect(v.own.length).toBeGreaterThanOrEqual(5);
  });
  it.each(v.pick.map((c) => [c.name, c] as const))("pick: %s", (_n, c) => {
    const r = pickChargeEta(c.truck_mins, c.validity, c.soc_pct, c.limit_pct, c.ours_mins);
    expect(r.minutes).toBe(c.want_mins);
    expect(r.source ?? "").toBe(c.want_source === "estimate" ? "estimate" : c.want_source);
  });
  it.each(v.own.map((c) => [c.name, c] as const))("own: %s", (_n, c) => {
    expect(ownChargeEtaMins(c.soc_pct, c.limit_pct, c.pack_kwh, c.power_kw)).toBe(c.want_mins);
  });
});
