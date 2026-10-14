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

import { beforeEach, vi } from "vitest";
import { readThemeChoice, resolveLight, THEME_KEY, writeThemeChoice } from "./theme";

describe("theme choice", () => {
  beforeEach(() => window.localStorage.clear());

  it("defaults to System and persists Light and Dark", () => {
    expect(readThemeChoice()).toBe("system");
    writeThemeChoice("light");
    expect(window.localStorage.getItem(THEME_KEY)).toBe("light");
    expect(readThemeChoice()).toBe("light");
    writeThemeChoice("dark");
    expect(readThemeChoice()).toBe("dark");
    writeThemeChoice("system");
    expect(window.localStorage.getItem(THEME_KEY)).toBeNull();
  });
  it("falls back to System when storage is blocked", () => {
    const spy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(readThemeChoice()).toBe("system");
    spy.mockRestore();
  });
  it("ignores junk values", () => {
    window.localStorage.setItem(THEME_KEY, "sepia");
    expect(readThemeChoice()).toBe("system");
  });
  it("resolves each mode against the OS preference", () => {
    expect(resolveLight("light", false)).toBe(true);
    expect(resolveLight("dark", true)).toBe(false);
    expect(resolveLight("system", true)).toBe(true);
    expect(resolveLight("system", false)).toBe(false);
  });
});
