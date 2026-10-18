
export function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-neutral-500">{label}</dt>
      <dd className="font-mono text-neutral-100">{value}</dd>
    </div>
  );
}

// LONG_DAY_THRESHOLD_MI / LONG_DAY_THRESHOLD_HR drive the "long day"
// chip on each day card. Picked from the road-trip rule of thumb that
// driving more than ~10 hours or ~600 miles in a single day is fatigue
// territory.
