import { useEffect, useState } from "react";

// The look's CSS layer is scoped under html[data-ui="next"] (index.css). It is the only look now
// (the old one was removed), so every shell sets the attribute.
export function useApplyUiNext(): void {
  useEffect(() => {
    const root = document.documentElement;
    root.setAttribute("data-ui", "next");
    return () => root.removeAttribute("data-ui");
  }, []);
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
