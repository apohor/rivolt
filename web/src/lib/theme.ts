// Light theme follows the system preference, and only inside the
// money_tab_enabled shell. With the flag off the attribute is never
// set, so the app stays dark exactly as before.

import { useEffect } from "react";

// The shell flag resolves after first paint, which would flash dark ->
// light on every load. The last resolved value is cached and index.html
// applies the theme from it before React mounts. Storage may throw
// (private windows, blocked site data): fall back to dark.
export const FLAG_CACHE_KEY = "rivolt.money_tab_enabled";

export function cacheShellFlag(enabled: boolean) {
  try {
    window.localStorage.setItem(FLAG_CACHE_KEY, enabled ? "1" : "0");
  } catch {
    /* ignore */
  }
  if (!enabled) applyTheme(false);
}

const DARK_THEME_COLOR = "#0a0a0a";
const LIGHT_THEME_COLOR = "#fafafa";

export function applyTheme(light: boolean, root: HTMLElement = document.documentElement) {
  if (light) root.setAttribute("data-theme", "light");
  else root.removeAttribute("data-theme");
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute("content", light ? LIGHT_THEME_COLOR : DARK_THEME_COLOR);
}

export function useSystemTheme(enabled: boolean) {
  useEffect(() => {
    if (!enabled || typeof window === "undefined" || !window.matchMedia) return;
    const mq = window.matchMedia("(prefers-color-scheme: light)");
    const sync = () => applyTheme(mq.matches);
    sync();
    mq.addEventListener("change", sync);
    return () => {
      mq.removeEventListener("change", sync);
      applyTheme(false);
    };
  }, [enabled]);
}
