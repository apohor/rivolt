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

import { cacheShellFlag, FLAG_CACHE_KEY } from "./theme";

describe("cacheShellFlag", () => {
  it("stores the flag and clears the theme when off", () => {
    document.documentElement.setAttribute("data-theme", "light");
    cacheShellFlag(true);
    expect(window.localStorage.getItem(FLAG_CACHE_KEY)).toBe("1");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    cacheShellFlag(false);
    expect(window.localStorage.getItem(FLAG_CACHE_KEY)).toBe("0");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });
});
