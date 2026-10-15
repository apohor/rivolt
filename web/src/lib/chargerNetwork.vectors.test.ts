import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { choosePin, displayNetwork, networkKey, type PoiLike } from "./chargerNetwork";

// Shared with the Go port (internal/dcfcrates/pin.go): both must pass
// every vector. Change a vector before changing either implementation.
const file = path.join(__dirname, "../../../internal/dcfcrates/testdata/choose_pin_vectors.json");
const v = JSON.parse(fs.readFileSync(file, "utf8")) as {
  network_key: { parts: (string | null)[]; want: string | null }[];
  display_network: { in: string; want: string }[];
  choose_pin: { name: string; poi: PoiLike | null; known: string | null; isDC: boolean; want: { snap: boolean; label: string } }[];
};

describe("shared charger-network vectors", () => {
  it.each(v.network_key.map((c) => [JSON.stringify(c.parts), c] as const))("networkKey %s", (_n, c) => {
    expect(networkKey(...c.parts)).toBe(c.want);
  });
  it.each(v.display_network.map((c) => [JSON.stringify(c.in), c] as const))("displayNetwork %s", (_n, c) => {
    expect(displayNetwork(c.in)).toBe(c.want);
  });
  it.each(v.choose_pin.map((c) => [c.name, c] as const))("choosePin: %s", (_n, c) => {
    expect(choosePin(c.poi, c.known, c.isDC)).toEqual(c.want);
  });
});
