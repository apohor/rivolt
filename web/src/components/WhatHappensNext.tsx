import { useEffect, useRef } from "react";

// The connected step of onboarding under design key signup_v2: what the user will see, and when. Each time is the code's:
// live state is read the moment Now opens (internal/api/vehicles.go, the per-vehicle state handler); a drive is saved 10 s after the
// truck parks (driveCloseDebounce, internal/rivian/recorder.go); Rivolt records from the connection on and does not pull older drives.
export function WhatHappensNext() {
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);
  return (
    <section data-what-next className="mt-4 space-y-2 rounded-lg border border-neutral-800 bg-neutral-900/40 p-4 text-sm text-neutral-300">
      <h3 ref={heading} tabIndex={-1} className="text-sm font-semibold text-neutral-100 outline-hidden">
        You're connected. What happens next
      </h3>
      <ul className="space-y-1.5">
        <li>
          <strong className="text-neutral-100">Right away:</strong> battery, range and where it's parked show on Now.
        </li>
        <li>
          <strong className="text-neutral-100">When you drive:</strong> a drive is saved about 10 seconds after you park. A charge is saved when it ends.
        </li>
        <li>
          <strong className="text-neutral-100">Older drives and charges</strong> are not pulled from Rivian; Rivolt records from now on.
        </li>
        <li>
          <strong className="text-neutral-100">Next:</strong> a short setup on Now (home, electricity price, notifications). Every step can be skipped.
        </li>
      </ul>
    </section>
  );
}
