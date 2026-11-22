import { useEffect, useRef } from "react";
import { Link } from "react-router-dom";
import { useDemoEnabled } from "../lib/config";

// The choice after sign-up (design key signup_v2): add the truck, or look at the sample truck first. The sample is the public demo
// (in-app /demo where the install has it), opened with ?from=app so its banner can bring the user back. Nothing done there is saved.
const DEMO_HOST = "https://demo.rivolt.dev";
const card = "block rounded-xl border p-4 transition hover:border-emerald-500";

export function SignupChoice({ onAdd }: { onAdd: () => void }) {
  const demoEnabled = useDemoEnabled();
  const hosted = /(^|\.)rivolt\.dev$/.test(window.location.hostname);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);
  const sample = demoEnabled || hosted;
  return (
    <div className="space-y-4">
      <h2 ref={heading} tabIndex={-1} className="text-base font-semibold text-neutral-100 outline-hidden">
        Your account is ready
      </h2>
      <p className="text-sm text-neutral-400">Add your truck now{sample ? ", or look around with a sample truck first. You can do both." : "."}</p>
      <button type="button" onClick={onAdd} className={`${card} w-full border-emerald-600 text-left`}>
        <span className="block text-sm font-semibold text-neutral-100">Add my truck</span>
        <span className="mt-1 block text-sm text-neutral-400">
          Sign in to Rivian on the next screen. Your battery, range and where it's parked show up right away; drives and charges are recorded from then on.
        </span>
        <span className="mt-2 inline-block rounded-full border border-emerald-700 px-2 py-0.5 text-xs text-emerald-300">About 2 minutes</span>
      </button>
      {sample && (
        <a
          href={demoEnabled ? "/demo?from=app" : `${DEMO_HOST}/demo?from=app`}
          className={`${card} border-neutral-700`}
        >
          <span className="block text-sm font-semibold text-neutral-100">Try it with a sample truck</span>
          <span className="mt-1 block text-sm text-neutral-400">
            Opens the demo: a generated truck with sample drives, charges and planned trips. Read-only, and nothing you do there is saved to your account.
          </span>
        </a>
      )}
      <p className="text-xs text-neutral-400">
        You can add your truck any time from Now or Settings. Rivolt is unofficial and not affiliated with Rivian.{" "}
        <Link to="/safe-to-connect" className="text-emerald-300 underline underline-offset-2">Is it safe to connect?</Link>
      </p>
    </div>
  );
}
