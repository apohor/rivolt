import type { ReactNode } from "react";

// The truck picture with its plug state (a grey plug badge while plugged in and not charging; while power flows (> 0 kW) a soft
// green glow pulsing along the base (about 2 s) and a green bolt badge. Reduced motion keeps the glow, still. The badge is the screen reader's cue.
export function plugLabel(kw: number | null | undefined): string {
  return (kw ?? 0) > 0 ? `Charging, ${Math.round((kw as number) * 10) / 10} kW` : "Plugged in, not charging";
}

export function PlugPicture({ kw, className = "", children }: { kw: number | null | undefined; className?: string; children: ReactNode }) {
  const charging = (kw ?? 0) > 0;
  return (
    <div className={`relative ${className}`} data-testid="plug-picture" data-plug={charging ? "charging" : "plugged"}>
      {children}
      {charging && (
        <span
          aria-hidden="true"
          data-testid="plug-glow"
          className="pointer-events-none absolute inset-x-[12%] bottom-0 h-3 rounded-full bg-emerald-400/50 blur-md motion-safe:animate-pulse"
        />
      )}
      <span
        role="img"
        aria-label={plugLabel(kw)}
        data-testid="plug-badge"
        className={`absolute right-1 top-1 flex h-6 w-6 items-center justify-center rounded-full ${charging ? "bg-emerald-500/20 text-emerald-400" : "bg-neutral-700/60 text-neutral-300"}`}
      >
        <svg viewBox="0 0 24 24" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          {charging ? <path d="M13 2 4 14h7l-1 8 9-12h-7z" fill="currentColor" /> : <path d="M9 2v5M15 2v5M6 7h12v4a6 6 0 0 1-12 0zM12 17v5" />}
        </svg>
      </span>
    </div>
  );
}
