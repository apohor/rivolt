import { useEffect, useRef } from "react";
import { reducedMotion } from "./motion";
import deskShot from "../../assets/landing/desk-plan.webp";
import cardShot from "../../assets/landing/plan-card.webp";

// Real planner screenshots with the radar layer on. The desktop shot tips up
// flat as it scrolls in, and the plan-card close-up rises beside it; both rest
// flat (and stay flat with reduced motion).
export default function PlannerShots() {
  const grid = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = grid.current;
    if (!el || reducedMotion()) return;
    const browser = el.querySelector<HTMLElement>(".lp-browser")!;
    const card = el.querySelector<HTMLElement>(".lp-card-shot")!;
    let raf = 0;
    const tip = () => {
      raf = 0;
      const r = el.getBoundingClientRect(), vh = innerHeight;
      const k = Math.max(0, Math.min(1, (vh - r.top) / (vh * 0.85)));
      browser.style.setProperty("--tilt", `${(22 * (1 - k)).toFixed(2)}deg`);
      browser.style.setProperty("--zoom", (0.9 + 0.1 * k).toFixed(3));
      card.style.setProperty("--lift", `${(90 * (1 - k)).toFixed(1)}px`);
    };
    const onScroll = () => {
      if (!raf) raf = requestAnimationFrame(tip);
    };
    tip();
    addEventListener("scroll", onScroll, { passive: true });
    addEventListener("resize", onScroll);
    return () => {
      removeEventListener("scroll", onScroll);
      removeEventListener("resize", onScroll);
      cancelAnimationFrame(raf);
    };
  }, []);
  return (
    <div ref={grid} className="lp-plan-grid">
      <figure className="lp-browser" style={{ margin: 0 }}>
        <div className="lp-bar" aria-hidden="true">
          <i />
          <i />
          <i />
          <span>rivolt.dev/trips/plan</span>
        </div>
        <img
          src={deskShot}
          alt="Rivolt trip planner on a desktop browser: Cleveland to Albany with two charging stops, temperature and wind on the route, and the live weather radar layer on the map"
          loading="lazy"
          decoding="async"
          width={1600}
          height={1000}
        />
      </figure>
      <figure className="lp-card-shot" style={{ margin: 0 }}>
        <img
          src={cardShot}
          alt="The same plan on a phone: radar map, then the itinerary with each charging stop's minutes, charge in and out, temperature and wind"
          loading="lazy"
          decoding="async"
          width={920}
          height={2880}
        />
      </figure>
    </div>
  );
}
