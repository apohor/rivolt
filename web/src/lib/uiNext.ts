import { useEffect, useState } from "react";

// Motion helpers for the look: reduced-motion detection and the count-up on changing numbers.

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
