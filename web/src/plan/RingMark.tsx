import { RING, ringArc } from "./chargeRing";

// plan_v2: the charge ring as React (the itinerary's rail); the map builds the same drawing as markup (chargeRingSvg).
export function ChargeRing({ size, arrival, departure, on = false }: { size: number; arrival: number | null | undefined; departure: number | null | undefined; on?: boolean }) {
  const arc = ringArc(arrival, departure);
  return (
    <span data-charge-ring data-on={on ? "" : undefined}>
      <svg width={size} height={size} viewBox={`0 0 ${RING.view} ${RING.view}`} aria-hidden="true" focusable="false" style={{ display: "block" }}>
        <circle cx={RING.mid} cy={RING.mid} r={RING.mid} fill="var(--cr-disc)" />
        <circle cx={RING.mid} cy={RING.mid} r={RING.r} fill="none" stroke="var(--cr-track)" strokeWidth={RING.stroke} />
        {arc && (
          <circle
            cx={RING.mid}
            cy={RING.mid}
            r={RING.r}
            fill="none"
            stroke="var(--cr-arc)"
            strokeWidth={RING.stroke}
            strokeLinecap="round"
            strokeDasharray={`${(arc.length * RING.c).toFixed(2)} ${RING.c.toFixed(2)}`}
            transform={`rotate(${(arc.start * 360 - 90).toFixed(2)} ${RING.mid} ${RING.mid})`}
          />
        )}
        <path d={RING.bolt} fill="var(--cr-arc)" />
      </svg>
    </span>
  );
}
