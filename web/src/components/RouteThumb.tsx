// A drive's route as a tiny SVG: its shape and its two ends, drawn from the few points the server sends (lib/routeThumbs.ts). No map, no tiles,
// no library: a path in a viewBox. The box is always there, so a row does not jump when its thumbnail arrives; a drive with no route leaves it
// empty.
import { useEffect, useRef, useSyncExternalStore } from "react";
import { decodePolyline } from "../lib/polyline";
import { routeThumb, subscribeRouteThumbs, wantRouteThumb } from "../lib/routeThumbs";

const W = 64;
const H = 40;
const PAD = 6;

// thumbPath is the SVG path of a route fitted into the box, with its first and last point. Longitude is scaled by cos(latitude) so the shape is
// not stretched. null when there is nothing to draw (under two points, or no extent).
export function thumbPath(points: [number, number][], w = W, h = H, pad = PAD): { d: string; a: [number, number]; b: [number, number] } | null {
  if (points.length < 2) return null;
  const k = Math.cos((points.reduce((s, p) => s + p[0], 0) / points.length) * (Math.PI / 180));
  const xs = points.map((p) => p[1] * k);
  const ys = points.map((p) => p[0]);
  const minX = Math.min(...xs), maxX = Math.max(...xs), minY = Math.min(...ys), maxY = Math.max(...ys);
  const spanX = maxX - minX, spanY = maxY - minY;
  if (!(spanX > 0 || spanY > 0)) return null;
  const sc = Math.min(spanX > 0 ? (w - 2 * pad) / spanX : Infinity, spanY > 0 ? (h - 2 * pad) / spanY : Infinity);
  const ox = (w - spanX * sc) / 2, oy = (h - spanY * sc) / 2;
  const xy = points.map((_, i) => [ox + (xs[i] - minX) * sc, h - (oy + (ys[i] - minY) * sc)] as [number, number]);
  const d = xy.map((p, i) => `${i ? "L" : "M"}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join("");
  return { d, a: xy[0], b: xy[xy.length - 1] };
}

export function RouteThumb({ driveId }: { driveId: string }) {
  const ref = useRef<HTMLSpanElement | null>(null);
  const polyline = useSyncExternalStore(subscribeRouteThumbs, () => routeThumb(driveId));
  // Ask for the thumbnail when the box comes near the screen (a long list asks only for what it shows).
  useEffect(() => {
    const el = ref.current;
    if (!el || polyline !== undefined) return;
    if (typeof IntersectionObserver === "undefined") {
      wantRouteThumb(driveId);
      return;
    }
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) {
        wantRouteThumb(driveId);
        io.disconnect();
      }
    }, { rootMargin: "300px" });
    io.observe(el);
    return () => io.disconnect();
  }, [driveId, polyline]);
  const shape = polyline ? thumbPath(decodePolyline(polyline)) : null;
  return (
    <span ref={ref} className="route-thumb" data-route-thumb={shape ? "drawn" : "empty"} aria-hidden>
      {shape && (
        <svg viewBox={`0 0 ${W} ${H}`} width={W} height={H}>
          <path d={shape.d} className="route-thumb-casing" />
          <path d={shape.d} className="route-thumb-line" />
          <circle cx={shape.a[0]} cy={shape.a[1]} r="3" className="route-thumb-start" />
          <circle cx={shape.b[0]} cy={shape.b[1]} r="3" className="route-thumb-end" />
        </svg>
      )}
    </span>
  );
}
