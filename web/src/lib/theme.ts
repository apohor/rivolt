// Theme: System (default) / Light / Dark, only inside the
// money_tab_enabled shell. With the flag off the attribute is never
// set, so the app stays dark exactly as before.

import { useCallback, useEffect, useState } from "react";

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

// The user's choice, per device. System follows prefers-color-scheme.
export type ThemeChoice = "system" | "light" | "dark";
export const THEME_KEY = "rivolt.theme";
const CHANGE_EVENT = "rivolt:theme-choice";

export function readThemeChoice(): ThemeChoice {
  try {
    const v = window.localStorage.getItem(THEME_KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

export function writeThemeChoice(c: ThemeChoice) {
  try {
    if (c === "system") window.localStorage.removeItem(THEME_KEY);
    else window.localStorage.setItem(THEME_KEY, c);
  } catch {
    /* blocked storage: the choice still applies for this session via the event */
  }
  window.dispatchEvent(new CustomEvent(CHANGE_EVENT, { detail: c }));
}

// resolveLight: Light means light, Dark means dark, System follows the OS.
export function resolveLight(choice: ThemeChoice, osPrefersLight: boolean): boolean {
  return choice === "light" ? true : choice === "dark" ? false : osPrefersLight;
}

// useThemeChoice reads and sets the choice; every user of it (the
// Settings control and the applier) stays in sync.
export function useThemeChoice(): [ThemeChoice, (c: ThemeChoice) => void] {
  const [choice, setChoice] = useState<ThemeChoice>(() => readThemeChoice());
  useEffect(() => {
    const on = (e: Event) => setChoice((e as CustomEvent<ThemeChoice>).detail ?? readThemeChoice());
    window.addEventListener(CHANGE_EVENT, on);
    return () => window.removeEventListener(CHANGE_EVENT, on);
  }, []);
  const set = useCallback((c: ThemeChoice) => writeThemeChoice(c), []);
  return [choice, set];
}

// useSystemTheme applies the theme inside the shell: the stored choice,
// following the OS only while the choice is System. Flag off: never set.
export function useSystemTheme(enabled: boolean) {
  const [choice] = useThemeChoice();
  useEffect(() => {
    if (!enabled || typeof window === "undefined" || !window.matchMedia) return;
    const mq = window.matchMedia("(prefers-color-scheme: light)");
    const sync = () => applyTheme(resolveLight(choice, mq.matches));
    sync();
    if (choice === "system") mq.addEventListener("change", sync);
    return () => {
      mq.removeEventListener("change", sync);
      applyTheme(false);
    };
  }, [enabled, choice]);
}
