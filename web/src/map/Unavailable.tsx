// The two plain-words states every MapLibre map shares: no WebGL in this browser, and no tile archive on this server.

export const TILES_UNAVAILABLE_TEXT = "Map tiles unavailable on this server";

// MapUnavailable is what the card shows when this browser cannot draw WebGL: plain words, the box kept so the layout does not jump.
export function MapUnavailable({ height, note = "The drive's figures and charts below are unaffected." }: { height: number; note?: string }) {
  return (
    <div
      role="status"
      data-map-unavailable
      className="flex items-center justify-center rounded-lg border border-neutral-800 bg-neutral-900/50 px-4 text-center text-sm text-neutral-400"
      style={{ height }}
    >
      The map can&apos;t be drawn in this browser (WebGL is off or unsupported). {note}
    </div>
  );
}
