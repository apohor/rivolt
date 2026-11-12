import { useEffect, useRef } from "react";
import { reducedMotion } from "./motion";

// The hero's sample trip card. Every figure is the demo truck's planned
// "Las Vegas weekend" (internal/demo/trips/7041ba4f-….plan.json): leaves the
// Rose Bowl at 90 % at 8:00 AM, 102 mi to Barstow (89 min), 18 min at 150 kW
// from 58 to 80 %, 162 mi on to Las Vegas, arrives 12:04 PM at 30 %, $13.64.
// The drifting rain cells are drawn, an animation only; the planner shots
// below are the real radar.
export const SAMPLE = { miles: 264, stopMile: 102, cost: 13.64, arrivePct: 30, departMin: 8 * 60 };
const D1 = 3400, CH = 1900, D2 = 4300, HOLD = 2600, TOTAL = D1 + CH + D2 + HOLD;
const ROUTE = "M92 238 C134 244 164 244 172 228 S190 178 204 159 S256 140 274 130 S312 104 328 98 S372 92 382 80 S404 36 410 18";

function ease(x: number) {
  return x < 0.5 ? 2 * x * x : 1 - Math.pow(-2 * x + 2, 2) / 2;
}

export function clockParts(min: number): [string, string] {
  const t = SAMPLE.departMin + Math.round(min);
  const h = Math.floor(t / 60), m = t % 60;
  return [`${h % 12 || 12}:${m < 10 ? "0" : ""}${m}`, h >= 12 ? "PM" : "AM"];
}

// One clock drives the route, the truck dot and the readouts. ms past the
// loop's end is the arrived state, which is also what renders at rest.
export function tripAt(ms: number) {
  if (ms < D1) {
    const a = ease(ms / D1);
    return { min: 89 * a, mile: 102 * a, pct: 90 - 32 * a, cost: 0, label: "Driving to Barstow", charging: false };
  }
  if (ms < D1 + CH) {
    const b = (ms - D1) / CH;
    return { min: 89 + 18 * b, mile: 102, pct: 58 + 22 * b, cost: SAMPLE.cost * b, label: "Charging · 150 kW", charging: true };
  }
  if (ms < D1 + CH + D2) {
    const c = ease((ms - D1 - CH) / D2);
    return { min: 107 + 137 * c, mile: 102 + 162 * c, pct: 80 - 50 * c, cost: SAMPLE.cost, label: "Driving to Las Vegas", charging: false };
  }
  return { min: 244, mile: 264, pct: 30, cost: SAMPLE.cost, label: "Arrived with 30%", charging: false };
}

export default function HeroTrip() {
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = root.current;
    if (!el) return;
    const q = <T extends Element>(s: string) => el.querySelector(s) as T;
    const base = q<SVGPathElement>("[data-route=base]");
    const live = q<SVGPathElement>("[data-route=live]");
    const glow = q<SVGPathElement>("[data-route=glow]");
    const truck = q<SVGGElement>("[data-truck]");
    const halo = q<SVGCircleElement>("[data-halo]");
    const state = q<HTMLElement>("[data-state]");
    const [clock, ampm, mi, pct, cost, label] = ["clock", "ampm", "mi", "pct", "cost", "label"].map((k) => q<HTMLElement>(`[data-hud=${k}]`));
    // getTotalLength is missing in jsdom; the card then stays in its rest state.
    if (typeof base.getTotalLength !== "function") return;
    const L = base.getTotalLength();
    const sp = base.getPointAtLength((L * SAMPLE.stopMile) / SAMPLE.miles);
    q<SVGGElement>("[data-stop]").setAttribute("transform", `translate(${sp.x},${sp.y})`);
    const stopLabel = q<SVGTextElement>("[data-stop-label]");
    stopLabel.setAttribute("x", String(sp.x + 18));
    stopLabel.setAttribute("y", String(sp.y + 20));

    const frame = (ms: number) => {
      const s = tripAt(ms);
      const f = s.mile / SAMPLE.miles, p = base.getPointAtLength(L * f);
      live.setAttribute("stroke-dashoffset", String(1 - f));
      glow.setAttribute("stroke-dashoffset", String(1 - f));
      truck.setAttribute("transform", `translate(${p.x},${p.y})`);
      const [c, ap] = clockParts(s.min);
      clock.textContent = c;
      ampm.textContent = ap;
      mi.textContent = String(Math.round(s.mile));
      pct.textContent = String(Math.round(s.pct));
      cost.textContent = `$${s.cost.toFixed(2)}`;
      label.textContent = s.label;
      state.dataset.charging = String(s.charging);
      halo.setAttribute("r", String(s.charging ? 15 + 6 * Math.sin(ms / 120) : 15));
    };
    frame(TOTAL);
    if (reducedMotion() || typeof IntersectionObserver === "undefined") return;

    // Runs only while the card is on screen; resumes where it left off.
    let raf = 0, last = 0, t = 0;
    const tick = (now: number) => {
      t = (t + Math.min(now - last, 100)) % TOTAL;
      last = now;
      frame(t);
      raf = requestAnimationFrame(tick);
    };
    const io = new IntersectionObserver(([e]) => {
      cancelAnimationFrame(raf);
      if (e.isIntersecting) {
        last = performance.now();
        raf = requestAnimationFrame(tick);
      }
    });
    io.observe(el);
    return () => {
      io.disconnect();
      cancelAnimationFrame(raf);
    };
  }, []);

  const [c, ap] = clockParts(244);
  return (
    <div ref={root} className="lp-trip" role="group" aria-label="Sample trip from Pasadena to Las Vegas, from the demo truck">
      <div className="lp-trip-head">
        <span className="tag">Sample trip</span>
        <span className="lp-state" data-state data-charging="false">
          <i aria-hidden="true" />
          <span data-hud="label" aria-hidden="true">Arrived with 30%</span>
        </span>
      </div>
      <svg className="lp-map" viewBox="0 0 520 300" role="img" aria-label="Route from Pasadena to Las Vegas with one charging stop in Barstow: 18 minutes, 58 to 80 percent">
        <defs>
          <pattern id="lp-dots" width="20" height="20" patternUnits="userSpaceOnUse">
            <circle cx="1" cy="1" r="1" fill="#161a20" />
          </pattern>
          {/* Soft-edged rain cells without SVG filters: a filter on a moving
              group is re-rasterised every frame, which cost iPhone Safari a
              quarter of its frame rate. */}
          {[
            ["g", "#22c55e"],
            ["y", "#eab308"],
            ["o", "#f97316"],
          ].map(([id, c]) => (
            <radialGradient key={id} id={`lp-wx-${id}`}>
              <stop offset="0" stopColor={c} />
              <stop offset=".6" stopColor={c} stopOpacity=".75" />
              <stop offset="1" stopColor={c} stopOpacity="0" />
            </radialGradient>
          ))}
        </defs>
        <rect width="520" height="300" fill="url(#lp-dots)" />
        <path d="M-10 250 C60 215 90 290 150 270 S250 300 330 262 S470 230 540 258" fill="none" stroke="#14181e" strokeWidth="1.5" />
        <path d="M-10 120 C80 90 120 150 200 118 S330 40 400 60 S500 20 540 30" fill="none" stroke="#14181e" strokeWidth="1.5" />
        <path d="M-10 185 C70 160 130 205 210 180 S350 120 430 135 S500 110 540 118" fill="none" stroke="#12151a" strokeWidth="1.5" />
        <g transform="translate(560,60)">
          <g className="lp-cell">
            <ellipse rx="92" ry="48" fill="url(#lp-wx-g)" opacity=".34" />
            <ellipse cx="8" cy="-2" rx="56" ry="27" fill="url(#lp-wx-y)" opacity=".42" />
            <ellipse cx="16" cy="-4" rx="24" ry="11" fill="url(#lp-wx-o)" opacity=".55" />
          </g>
        </g>
        <g transform="translate(640,170)">
          <g className="lp-cell c2">
            <ellipse rx="60" ry="30" fill="url(#lp-wx-g)" opacity=".3" />
            <ellipse cx="-6" cy="3" rx="28" ry="13" fill="url(#lp-wx-y)" opacity=".4" />
          </g>
        </g>
        <path data-route="base" d={ROUTE} fill="none" stroke="#2e3239" strokeWidth="5" strokeLinecap="round" />
        <path data-route="glow" d={ROUTE} fill="none" stroke="#5ee9b5" strokeWidth="12" strokeLinecap="round" opacity=".18" pathLength={1} strokeDasharray="1" strokeDashoffset="0" />
        <path data-route="live" d={ROUTE} fill="none" stroke="#5ee9b5" strokeWidth="4" strokeLinecap="round" pathLength={1} strokeDasharray="1" strokeDashoffset="0" />
        <circle cx="92" cy="238" r="6" fill="#060a10" stroke="#5ee9b5" strokeWidth="3" />
        <text x="60" y="264">Pasadena</text>
        <g data-stop transform="translate(214,154)">
          <circle data-halo r="15" fill="#f59e0b" opacity=".16" />
          <circle r="9" fill="#f59e0b" />
          <path d="M1.5-5.5-3 1h3l-1 4.5L3.5-1h-3z" fill="#1c1200" />
        </g>
        <text data-stop-label x="232" y="174">18 min · 58→80%</text>
        <circle cx="410" cy="18" r="6" fill="#060a10" stroke="#f43f5e" strokeWidth="3" />
        <text x="424" y="24">Las Vegas</text>
        <g transform="translate(14,14)">
          <rect width="150" height="24" rx="12" fill="#0e1115" stroke="#2e3239" />
          <circle cx="13" cy="12" r="3.5" fill="#22c55e" />
          <text x="23" y="16">Weather and elevation</text>
        </g>
        <g data-truck transform="translate(410,18)">
          <circle r="12" fill="#5ee9b5" opacity=".25" />
          <circle r="5.5" fill="#fff" />
        </g>
      </svg>
      {/* Screen readers get the arrived figures once; the moving readouts are hidden from them. */}
      <p className="sr-only">{`Arrives ${c} ${ap} after 264 miles with 30% battery; charging $13.64.`}</p>
      <dl className="lp-hud" aria-hidden="true">
        <div>
          <dt>Clock</dt>
          <dd>
            <span data-hud="clock">{c}</span>
            <small data-hud="ampm">{ap}</small>
          </dd>
        </div>
        <div>
          <dt>Driven</dt>
          <dd>
            <span data-hud="mi">264</span>
            <small>mi</small>
          </dd>
        </div>
        <div>
          <dt>Battery</dt>
          <dd>
            <span data-hud="pct">30</span>
            <small>%</small>
          </dd>
        </div>
        <div>
          <dt>Charging</dt>
          <dd data-hud="cost">$13.64</dd>
        </div>
      </dl>
      <p className="lp-trip-foot">The demo truck&apos;s planned trip. Charging priced at the network&apos;s listed rate.</p>
    </div>
  );
}
