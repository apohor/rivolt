import { useEffect, useRef, useState } from "react";
import { reducedMotion } from "./motion";
import nowShot from "../../assets/landing/phone-now.webp";
import activityShot from "../../assets/landing/phone-activity.webp";
import moneyShot from "../../assets/landing/phone-money.webp";

const CHAPTERS = [
  {
    num: "Now",
    title: "Your truck, right now.",
    body: "Charge, range and cabin temperature at a glance.",
    shot: nowShot,
    alt: "Rivolt Now screen for the demo truck: 76 percent battery and 228 miles of range",
    halo: "rgba(41,213,165,.24)",
  },
  {
    num: "Activity",
    title: "Every drive and charge, logged.",
    body: "Route, distance, energy and cost. Recorded without you doing anything.",
    shot: activityShot,
    alt: "Rivolt Activity feed for the demo truck with drives and home charges",
    halo: "rgba(77,185,255,.24)",
  },
  {
    num: "Money",
    title: "What charging actually cost.",
    body: "A receipt for every session and a real cost per mile.",
    shot: moneyShot,
    alt: "Rivolt Money screen for the demo truck with the month's charging cost",
    halo: "rgba(245,158,11,.24)",
  },
];
const TILT = ["rotate(-2deg)", "rotate(1.5deg)", "rotate(-1deg)"];

// Desktop: a pinned phone switches screens as the three chapters scroll past.
// Phone width: each chapter shows its own screenshot inline instead.
export default function PhoneTour() {
  const [on, setOn] = useState(0);
  const chaps = useRef<(HTMLElement | null)[]>([]);
  const [still] = useState(reducedMotion);
  useEffect(() => {
    if (typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(
      (es) => {
        for (const e of es) if (e.isIntersecting) setOn(Number((e.target as HTMLElement).dataset.i));
      },
      { rootMargin: "-45% 0px -45% 0px" },
    );
    for (const c of chaps.current) if (c) io.observe(c);
    return () => io.disconnect();
  }, []);
  return (
    <div className="lp-tour">
      <div className="lp-stage" aria-hidden="true">
        <div className="lp-glow" style={{ "--halo": CHAPTERS[on].halo } as React.CSSProperties} />
        <div className="lp-phone" style={still ? undefined : { transform: TILT[on] }}>
          <div className="scr">
            {CHAPTERS.map((c, i) => (
              <img key={c.num} src={c.shot} alt="" data-on={String(i === on)} loading="lazy" decoding="async" width={720} height={1559} />
            ))}
          </div>
        </div>
      </div>
      <div>
        {CHAPTERS.map((c, i) => (
          <article
            key={c.num}
            ref={(el) => {
              chaps.current[i] = el;
            }}
            className="lp-chap"
            data-i={i}
            data-on={String(i === on)}
          >
            <span className="num">{c.num}</span>
            <h3>{c.title}</h3>
            <p>{c.body}</p>
            <img className="inline" src={c.shot} alt={c.alt} loading="lazy" decoding="async" width={720} height={1559} />
          </article>
        ))}
      </div>
    </div>
  );
}
