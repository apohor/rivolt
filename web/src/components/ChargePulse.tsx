import type { ReactNode } from "react";

// Design "charge_pulse": the charging cue on the Live and Now cards, in PlugPicture's language (soft green, about 2 s). While power flows (> 0 kW)
// a small bolt beside the kW figure, a glow at the live end of the progress bar and the battery line's dot pulse; plugged in and not charging
// is a still grey plug beside the status. Drawn with CSS only (index.css, .cp-*): reduced motion keeps it, still. The glyph is hidden from screen
// readers: the status text beside it is the cue, so nothing is announced as it pulses.
export type PulseState = "charging" | "plugged" | "none";

// Key off: always "none", so the markup is today's exactly.
export function chargePulseState(on: boolean, kw: number | null | undefined, plugged: boolean): PulseState {
  if (!on) return "none";
  if ((kw ?? 0) > 0) return "charging";
  return plugged ? "plugged" : "none";
}

export function PulseBolt({ state }: { state: PulseState }) {
  if (state === "none") return null;
  return (
    <span aria-hidden="true" data-testid="charge-pulse-bolt" data-pulse={state} className="cp-bolt">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        {state === "charging" ? <path d="M13 2 4 14h7l-1 8 9-12h-7z" fill="currentColor" /> : <path d="M9 2v5M15 2v5M6 7h12v4a6 6 0 0 1-12 0zM12 17v5" />}
      </svg>
    </span>
  );
}

// The bar with the glow over its live end; the bar alone (today's markup) unless charging.
export function withGlow(state: PulseState, pct: number, bar: ReactNode) {
  if (state !== "charging") return bar;
  return (
    <div className="relative">
      {bar}
      <PulseGlow pct={pct} />
    </div>
  );
}

// The glow at the progress bar's live end (the current percentage), outside the bar's own clipping.
export function PulseGlow({ pct }: { pct: number }) {
  return <span aria-hidden="true" data-testid="charge-pulse-glow" className="cp-glow" style={{ left: `${Math.min(100, Math.max(0, pct))}%` }} />;
}
