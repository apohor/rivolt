import { Link } from "react-router-dom";
import { useDemoEnabled } from "../lib/config";

// The two lines above the Rivian sign-in form (Sprint 12 item 5; growth's words, DA-checked): what Rivolt does with the login, and a way to
// look at a sample truck first. They replace "Sign in with the same email and password you use in the Rivian app…", which pointed at the
// main login while the form below recommends a driver account.
export const CONNECT_TRUST_LINE =
  "Connect a Rivian account and Rivolt reads your truck's data, such as drives, charges, battery and location. It sends no commands to your truck, and your Rivian password is used once to sign in and never stored.";
export const CONNECT_SAMPLE_LEAD = "Not ready to connect? ";
export const CONNECT_SAMPLE_LINK = "Look at a sample truck";
export const CONNECT_SAMPLE_TAIL = " with made-up data first; no account needed.";

// The demo is in-app (/demo) where the install has one, else the public demo host on rivolt.dev; nowhere else, so no dead link.
const DEMO_URL = "https://demo.rivolt.dev";

export function ConnectTrustLines({ className = "" }: { className?: string }) {
  const demoEnabled = useDemoEnabled();
  const hostedDemo = !demoEnabled && /(^|\.)rivolt\.dev$/.test(window.location.hostname);
  const linkClass = "text-emerald-300 underline underline-offset-2 hover:text-emerald-200";
  return (
    <div className={`space-y-2 ${className}`} data-connect-trust>
      <p>{CONNECT_TRUST_LINE}</p>
      {(demoEnabled || hostedDemo) && (
        <p data-connect-sample>
          {CONNECT_SAMPLE_LEAD}
          {demoEnabled ? (
            <Link to="/demo" className={linkClass}>{CONNECT_SAMPLE_LINK}</Link>
          ) : (
            <a href={DEMO_URL} className={linkClass}>{CONNECT_SAMPLE_LINK}</a>
          )}
          {CONNECT_SAMPLE_TAIL}
        </p>
      )}
    </div>
  );
}
