import { useEffect, useMemo, useState } from "react";
import Logo from "../components/Logo";
import { ElevationChart } from "../plan/ElevationChart";
import { usableProfile } from "../plan/elevation";
import { decodePolyline } from "../lib/polyline";
import { formatDuration } from "../lib/format";
import { useSystemTheme } from "../lib/theme";
import type { DistanceUnit } from "../lib/preferences";
import type { SharedTrip } from "../lib/api";
import { fetchSharedTrip, readShareToken, tripTime, type SharedTripResult } from "../lib/tripShare";

// /s#<token>: a trip someone shared, read-only, no account. The token is read from the address and cleared from it at once (see
// lib/tripShare.ts); this page sends no analytics event and no error report, and loads no map tiles (the route is drawn as plain SVG), so
// nothing about the visit leaves for another host. The server marks /s noindex and no-referrer; the meta tags below say the same.
const W = 360;
const H = 220;
const PAD = 16;

type Pt = [number, number];

// project fits every segment into one box (longitude scaled by cos(latitude) so the shape is not stretched). Segments stay separate lines:
// the server cuts the ends and any part near a hidden place, and joining them would draw a line across the gap.
export function projectSegments(segments: Pt[][], extra: Pt[]): { paths: string[]; at: (p: Pt) => [number, number] } | null {
  const all = [...segments.flat(), ...extra];
  if (all.length < 2) return null;
  const k = Math.cos((all.reduce((s, p) => s + p[0], 0) / all.length) * (Math.PI / 180));
  const xs = all.map((p) => p[1] * k);
  const ys = all.map((p) => p[0]);
  const minX = Math.min(...xs), maxX = Math.max(...xs), minY = Math.min(...ys), maxY = Math.max(...ys);
  const spanX = maxX - minX, spanY = maxY - minY;
  if (!(spanX > 0 || spanY > 0)) return null;
  const sc = Math.min(spanX > 0 ? (W - 2 * PAD) / spanX : Infinity, spanY > 0 ? (H - 2 * PAD) / spanY : Infinity);
  const ox = (W - spanX * sc) / 2, oy = (H - spanY * sc) / 2;
  const at = (p: Pt): [number, number] => [ox + (p[1] * k - minX) * sc, H - (oy + (p[0] - minY) * sc)];
  const paths = segments
    .filter((s) => s.length >= 2)
    .map((s) => s.map((p, i) => { const [x, y] = at(p); return `${i ? "L" : "M"}${x.toFixed(1)} ${y.toFixed(1)}`; }).join(""));
  return { paths, at };
}

function Shell({ children }: { children: React.ReactNode }) {
  useSystemTheme(true);
  useEffect(() => {
    const meta = [["robots", "noindex, nofollow"], ["referrer", "no-referrer"]].map(([name, content]) => {
      const m = document.createElement("meta");
      m.name = name;
      m.content = content;
      document.head.appendChild(m);
      return m;
    });
    return () => meta.forEach((m) => m.remove());
  }, []);
  return (
    <div className="min-h-full bg-neutral-950 text-neutral-200">
      <header className="border-b border-neutral-900">
        <div className="mx-auto flex max-w-3xl items-center justify-between gap-3 px-4 py-3">
          <a href="/" aria-label="Rivolt home" className="inline-flex items-center gap-2 text-sm font-semibold text-neutral-100"><Logo /></a>
          <span className="text-xs text-neutral-500">A shared trip</span>
        </div>
      </header>
      <main className="mx-auto max-w-3xl px-4 py-6 sm:py-10">{children}</main>
      <footer className="border-t border-neutral-900 text-xs text-neutral-500">
        <div className="mx-auto max-w-3xl px-4 py-4">Planned with <a href="/" className="underline underline-offset-2">Rivolt</a>. Rivolt is independent and not affiliated with Rivian.</div>
      </footer>
    </div>
  );
}

function Message({ title, body, onRetry }: { title: string; body: string; onRetry?: () => void }) {
  return (
    <div className="space-y-2 py-10 text-center" data-testid="shared-trip-message">
      <h1 className="text-xl font-semibold text-neutral-100">{title}</h1>
      <p className="text-sm text-neutral-400">{body}</p>
      {onRetry && <button type="button" onClick={onRetry} className="mt-2 rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500">Try again</button>}
    </div>
  );
}

export default function SharedTripPage() {
  // Read once; the address is cleared on the first read.
  const [token] = useState(() => readShareToken());
  const [result, setResult] = useState<SharedTripResult | "loading">(token ? "loading" : { kind: "notfound" });
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!token) return;
    const ac = new AbortController();
    setResult("loading");
    void fetchSharedTrip(token, ac.signal).then((r) => {
      if (!ac.signal.aborted) setResult(r);
    });
    return () => ac.abort();
  }, [token, attempt]);
  const retry = () => setAttempt((n) => n + 1);

  if (result === "loading") return <Shell><p className="py-10 text-center text-sm text-neutral-500" role="status">Opening the trip…</p></Shell>;
  if (result.kind === "notfound") return <Shell><Message title="This link isn't available" body="It may have expired, or the person who shared it turned it off. Ask them for a new one." /></Shell>;
  if (result.kind === "limited") return <Shell><Message title="Too many requests" body={`Wait ${result.retryAfter && result.retryAfter > 60 ? "a few minutes" : "a minute"} and try again.`} onRetry={retry} /></Shell>;
  if (result.kind === "busy") return <Shell><Message title="Rivolt is busy" body="The trip couldn't be opened just now. Try again in a moment." onRetry={retry} /></Shell>;
  if (result.kind === "error") return <Shell><Message title="Couldn't open the trip" body="Check your connection and try again." onRetry={retry} /></Shell>;
  return <Shell><SharedTripView trip={result.trip} /></Shell>;
}

function SharedTripView({ trip }: { trip: SharedTrip }) {
  const [unit, setUnit] = useState<DistanceUnit>("mi");
  const [cursor, setCursor] = useState<number | null>(null);
  useEffect(() => {
    const prev = document.title;
    document.title = `${trip.title} · Rivolt`;
    return () => { document.title = prev; };
  }, [trip.title]);
  const dist = (m: number) => `${Math.round(unit === "mi" ? m / 1609.344 : m / 1000).toLocaleString("en-US")} ${unit}`;
  const profile = useMemo(() => usableProfile(trip.elevation_profile), [trip.elevation_profile]);
  const map = useMemo(() => {
    const segs = trip.route.segments.map((s) => decodePolyline(s, 6));
    const marks: Pt[] = [[trip.start.lat, trip.start.lon], [trip.end.lat, trip.end.lon], ...trip.stops.map((s): Pt => [s.lat, s.lon])];
    return projectSegments(segs, marks);
  }, [trip]);
  const day = { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" } as const;
  const clock = { hour: "numeric", minute: "2-digit" } as const;
  const t = trip.totals;
  return (
    <article className="space-y-6" data-testid="shared-trip">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-neutral-50">{trip.title}</h1>
        <p className="mt-1 text-sm text-neutral-400">
          {trip.depart_at ? `Leaves ${tripTime(trip.depart_at, trip.tz, day)}` : `Planned ${tripTime(trip.planned_at, trip.tz, { month: "short", day: "numeric", year: "numeric" })}`}
          {" · "}
          <button type="button" onClick={() => setUnit(unit === "mi" ? "km" : "mi")} className="underline underline-offset-2 hover:text-neutral-200" data-testid="shared-trip-unit">Show {unit === "mi" ? "km" : "mi"}</button>
        </p>
      </div>
      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        {[
          ["Distance", dist(t.distance_m)],
          ["Driving", formatDuration(t.drive_s)],
          ["Charging", t.charge_s > 0 ? formatDuration(t.charge_s) : "None"],
          ["Arrive with", `${Math.round(t.arrival_soc)}%`],
        ].map(([k, v]) => (
          <div key={k} className="rounded-lg border border-neutral-800 bg-neutral-900/40 px-3 py-2">
            <dt className="text-xs text-neutral-500">{k}</dt>
            <dd className="text-lg font-semibold text-neutral-100">{v}</dd>
          </div>
        ))}
      </dl>
      {map && (
        <figure>
          <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`The route from the ${trip.start.label.toLowerCase()} to the ${trip.end.label.toLowerCase()}, with ${trip.stops.length} stops`} className="w-full rounded-lg border border-neutral-800 bg-neutral-900/40" data-testid="shared-trip-map">
            {map.paths.map((d, i) => <path key={i} d={d} data-segment fill="none" stroke="#34d399" strokeWidth={3} strokeLinecap="round" strokeLinejoin="round" />)}
            {trip.stops.map((s, i) => {
              const [x, y] = map.at([s.lat, s.lon]);
              return <circle key={i} cx={x} cy={y} r={s.kind === "charger" ? 5 : 4} data-stop={s.kind} fill={s.kind === "charger" ? "#fbbf24" : "#a3a3a3"} stroke="#0a0a0a" strokeWidth={1.5} />;
            })}
            {([["start", trip.start], ["end", trip.end]] as const).map(([k, a]) => {
              const [x, y] = map.at([a.lat, a.lon]);
              return <g key={k}><circle cx={x} cy={y} r={7} fill="none" stroke="#e5e5e5" strokeWidth={1.5} strokeDasharray="3 2" data-area={k} /><circle cx={x} cy={y} r={2.5} fill="#e5e5e5" /></g>;
            })}
          </svg>
          <figcaption className="mt-1 text-xs text-neutral-500">
            {trip.start.label} and {trip.end.label.toLowerCase()} are approximate, and the first and last few kilometers of the route are left out.
          </figcaption>
        </figure>
      )}
      {profile && (
        <div>
          <ElevationChart profile={profile} share={cursor} onShare={setCursor} distanceUnit={unit} />
        </div>
      )}
      <section aria-labelledby="shared-trip-stops">
        <h2 id="shared-trip-stops" className="text-lg font-semibold text-neutral-50">Stops</h2>
        {trip.stops.length === 0 ? (
          <p className="mt-1 text-sm text-neutral-400">No charging stop: the battery lasts the whole way.</p>
        ) : (
          <ol className="mt-2 space-y-2" data-testid="shared-trip-stops">
            {trip.stops.map((s, i) => (
              <li key={i} className="rounded-lg border border-neutral-800 px-3 py-2 text-sm">
                <div className="flex items-baseline justify-between gap-3">
                  <span className="font-medium text-neutral-100">{s.name || (s.kind === "charger" ? "Charging stop" : "Stop")}</span>
                  {s.at_m != null && <span className="shrink-0 text-xs text-neutral-500">{dist(s.at_m)} in</span>}
                </div>
                <div className="mt-0.5 text-neutral-400">
                  {s.arrive_at && <>Arrive {tripTime(s.arrive_at, trip.tz, clock)}{s.depart_at && <>, leave {tripTime(s.depart_at, trip.tz, clock)}</>} · </>}
                  {Math.round(s.arrive_soc)}% to {Math.round(s.depart_soc)}%
                  {s.charge_s ? <> · {formatDuration(s.charge_s)}</> : null}
                  {s.max_kw ? <> · up to {Math.round(s.max_kw)} kW</> : null}
                </div>
              </li>
            ))}
          </ol>
        )}
      </section>
      <p className="text-xs text-neutral-500">
        A frozen copy of the plan, made {tripTime(trip.planned_at, trip.tz, { month: "short", day: "numeric", year: "numeric" })} with {Math.round(trip.start_soc)}% to start. Real conditions change it. Terrain is shown for context only.
      </p>
    </article>
  );
}
