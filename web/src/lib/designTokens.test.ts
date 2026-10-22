import { readFileSync, writeFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildTokens, colour, cssVars, SOURCE_FILES } from "./designTokens";

// ios/design/tokens.json is what the iPhone app's Theme is built from. It must be
// exactly what the web's new look resolves to today: a changed colour, type size,
// glow or gauge value on the web fails here until the file is regenerated with
//   UPDATE_DESIGN_TOKENS=1 npx vitest run src/lib/designTokens.test.ts
// (and the app's Theme follows).
const TOKENS = "../ios/design/tokens.json";

function sources() {
  return Object.fromEntries(SOURCE_FILES.map((f) => [f, readFileSync(f, "utf8")]));
}

describe("iOS design tokens", () => {
  it("match the web's new look", () => {
    const built = buildTokens(sources());
    if (process.env.UPDATE_DESIGN_TOKENS) writeFileSync(TOKENS, JSON.stringify(built, null, 2) + "\n");
    expect(JSON.parse(readFileSync(TOKENS, "utf8"))).toEqual(built);
  });

  it("converts OKLCH to sRGB like the browser", () => {
    // Tailwind's published sRGB values for these.
    expect(colour("oklch(84.5% 0.143 164.978)").hex).toBe("#5EE9B5"); // emerald-300
    expect(colour("oklch(97% 0 none)").hex).toBe("#F5F5F5"); // neutral-100
    expect(colour("oklch(87.9% 0.169 91.605)").hex).toBe("#FFD230"); // amber-300
    expect(colour("oklch(100% 0 0 / 0.07)")).toEqual({ oklch: "oklch(100% 0 0 / 0.07)", hex: "#FFFFFF", alpha: 0.07 });
    expect(() => colour("#fff")).toThrow();
  });

  it("reads custom properties per selector, nested blocks included", () => {
    const vars = cssVars(`/* x */ :root { --a: 1; } @media (x) { html.b { --c: oklch(1% 0 0); } } @theme default { --d: 2px; }`);
    expect(vars.get(":root")).toEqual({ "--a": "1" });
    expect(vars.get("html.b")).toEqual({ "--c": "oklch(1% 0 0)" });
    expect(vars.get("@theme default")).toEqual({ "--d": "2px" });
  });

  it("names the source fragment a changed value broke", () => {
    const src = sources();
    src["src/components/ArcGauge.tsx"] = src["src/components/ArcGauge.tsx"].replace("const sweep = 240", "const sweep = 270");
    expect(() => buildTokens(src)).toThrow(/ArcGauge.tsx no longer contains: const sweep = 240/);
  });
});
