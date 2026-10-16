import { useEffect, useState } from "react";

// ui_theme_next: the "cinematic cockpit" visual layer, flag-gated and off by
// default. Off means exactly today's UI: everything lives under
// html[data-ui="next"] (see index.css) or in components that branch on
// useUiNext(). The server flag rides on GET /api/ledger/enabled
// ({ enabled, ui_theme_next }); ?ui=next / ?ui=off persist a per-device
// override so it can be compared side by side before the server flag exists.

const KEY = "rivolt.ui.next";
let serverFlag = false;
const listeners = new Set<() => void>();

export function setServerUiNext(v: boolean) {
  if (serverFlag === v) return;
  serverFlag = v;
  listeners.forEach((l) => l());
}

function readOverride(): "on" | "off" | null {
  try {
    const q = new URLSearchParams(window.location.search).get("ui");
    if (q === "next") window.localStorage.setItem(KEY, "on");
    else if (q === "off") window.localStorage.setItem(KEY, "off");
    else if (q === "reset") window.localStorage.removeItem(KEY);
    const v = window.localStorage.getItem(KEY);
    return v === "on" || v === "off" ? v : null;
  } catch {
    return null;
  }
}

export function uiNextEnabled(): boolean {
  const o = readOverride();
  return o ? o === "on" : serverFlag;
}

export function useUiNext(): boolean {
  const [on, setOn] = useState<boolean>(() => uiNextEnabled());
  useEffect(() => {
    const sync = () => setOn(uiNextEnabled());
    listeners.add(sync);
    sync();
    return () => {
      listeners.delete(sync);
    };
  }, []);
  return on;
}

// useApplyUiNext sets html[data-ui] for the CSS layer. Call once per shell.
export function useApplyUiNext(): boolean {
  const on = useUiNext();
  useEffect(() => {
    const root = document.documentElement;
    if (on) root.setAttribute("data-ui", "next");
    else root.removeAttribute("data-ui");
    return () => root.removeAttribute("data-ui");
  }, [on]);
  return on;
}

export function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

// useCountUp eases a displayed number toward `value` (200 ms ease-out) when
// it changes; instant under prefers-reduced-motion or when disabled.
export function useCountUp(value: number, enabled: boolean, ms = 220): number {
  const [shown, setShown] = useState(value);
  useEffect(() => {
    if (!enabled || prefersReducedMotion() || !Number.isFinite(value)) {
      setShown(value);
      return;
    }
    const from = shown;
    if (from === value) return;
    const t0 = performance.now();
    let raf = 0;
    const step = (t: number) => {
      const k = Math.min(1, (t - t0) / ms);
      const eased = 1 - Math.pow(1 - k, 3);
      setShown(from + (value - from) * eased);
      if (k < 1) raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, enabled, ms]);
  return shown;
}
