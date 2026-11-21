import type { ReactNode } from "react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useAppUrl, useDesignPreview, useSignupEnabled } from "../lib/config";

// ConnectLink sends a demo visitor to the real app's sign-up (or sign-in
// when sign-ups are closed). On the standalone demo deployment
// (features.app_url set) that is an external link to the real app; on the
// shared app it is an in-app route.
export function ConnectLink({ children, className, kind }: { children: ReactNode; className?: string; kind?: "signup" | "login" }) {
  const signupEnabled = useSignupEnabled();
  const appUrl = useAppUrl();
  // Sign-ups closed: "Connect" goes to sign in instead of a signup page
  // that would only say it's closed. null (loading) behaves as open.
  // On the standalone demo the server forces signup_enabled off (it has no
  // sign-ups of its own), so that flag says nothing about the real app: go
  // to its sign-up page, which handles a closed state itself.
  const path = kind === "login" || (!appUrl && signupEnabled === false) ? "/login" : "/signup";
  if (appUrl) {
    return (
      <a href={`${appUrl}${path}`} className={className} rel="noopener">
        {children}
      </a>
    );
  }
  return (
    <Link to={path} className={className}>
      {children}
    </Link>
  );
}

// "Back to Rivolt" (signup_v2): shown when the visitor came from the account's choice screen (?from=app), kept for the tab so it
// survives moving around the demo. Goes to the choice again; nothing done in the demo is saved.
function useFromApp(on: boolean): boolean {
  const [from, setFrom] = useState(false);
  useEffect(() => {
    if (!on) return;
    try {
      if (new URLSearchParams(window.location.search).get("from") === "app") sessionStorage.setItem("rivolt.demo_from_app", "1");
      setFrom(sessionStorage.getItem("rivolt.demo_from_app") === "1");
    } catch {
      /* storage blocked: no way back link */
    }
  }, [on]);
  return from;
}

// The sample-truck banner shown across the whole /demo shell.
export function DemoBanner() {
  // Design key signup_v2 (preview): the button says where it goes. Off, unchanged.
  const v2 = useDesignPreview("signup_v2");
  const fromApp = useFromApp(v2);
  const appUrl = useAppUrl();
  const back = "rounded-md border border-sky-700 px-3 py-1 text-xs font-semibold text-sky-100 hover:bg-sky-900/50";
  return (
    <div role="region" aria-label="Sample truck notice" className="border-b border-sky-800/60 bg-sky-950/40 px-4 py-2 text-sm text-sky-100">
      <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-2">
        <span>
          <span className="font-medium">Sample truck.</span> {v2 ? "Simulated data, read-only." : "Generated data, read-only. Connect your Rivian to see yours."}
        </span>
        <span className="flex flex-wrap items-center gap-2">
          {fromApp &&
            (appUrl ? (
              <a href={`${appUrl}/onboarding`} className={back}>Back to my account</a>
            ) : (
              <Link to="/onboarding" className={back}>Back to my account</Link>
            ))}
          <ConnectLink className="rounded-md bg-emerald-600 px-3 py-1 text-xs font-semibold text-white hover:bg-emerald-500">
            {v2 ? "Add my truck" : "Connect with a driver account"}
          </ConnectLink>
        </span>
      </div>
    </div>
  );
}

// ConnectHint: the standard "this needs your own truck" line that
// replaces a mutating control in the demo.
export function ConnectHint({ what }: { what: string }) {
  return (
    <p className="text-xs text-neutral-500" data-testid="connect-hint">
      <ConnectLink className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200">Connect your Rivian</ConnectLink> to {what}.
    </p>
  );
}
