import fs from "node:fs";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { appMapFlavor, onAppMapFlavor } from "./mapTheme";
import { applyTheme } from "./theme";

afterEach(() => {
  applyTheme(false);
  localStorage.clear();
});

describe("map flavor follows the app theme", () => {
  it("is dark without data-theme and light with it", () => {
    applyTheme(false);
    expect(appMapFlavor()).toBe("dark");
    applyTheme(true);
    expect(appMapFlavor()).toBe("light");
  });

  it("notifies on a live theme switch, once per change", async () => {
    const seen: string[] = [];
    const off = onAppMapFlavor((f) => seen.push(f));
    applyTheme(true);
    await Promise.resolve();
    applyTheme(true);
    applyTheme(false);
    await Promise.resolve();
    off();
    applyTheme(true);
    await Promise.resolve();
    expect(seen).toEqual(["light", "dark"]);
  });

  it("does not persist a per-map choice", () => {
    // The D/L override lives in DriveMap's closure. Nothing may write
    // the retired storage key.
    const src = fs.readFileSync(path.join(__dirname, "../components/DriveMap.tsx"), "utf8");
    expect(src).not.toContain("rivolt:basemap-flavor");
    expect(src).not.toMatch(/localStorage\.setItem\([^)]*flavor/i);
  });
});
