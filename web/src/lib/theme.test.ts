import { describe, expect, it } from "vitest";
import { applyTheme } from "./theme";

describe("applyTheme", () => {
  it("sets and clears data-theme", () => {
    const el = document.createElement("html");
    applyTheme(true, el);
    expect(el.getAttribute("data-theme")).toBe("light");
    applyTheme(false, el);
    expect(el.hasAttribute("data-theme")).toBe(false);
  });
});
