import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { useAppUrl, useSignupEnabled } from "../lib/config";

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

// The sample-truck banner shown across the whole /demo shell.
export function DemoBanner() {
  return (
    <div role="region" aria-label="Sample truck notice" className="border-b border-sky-800/60 bg-sky-950/40 px-4 py-2 text-sm text-sky-100">
      <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-2">
        <span>
          <span className="font-medium">Sample truck.</span> Generated data, read-only. Connect your Rivian to see
          yours.
        </span>
        <ConnectLink className="rounded-md bg-emerald-600 px-3 py-1 text-xs font-semibold text-white hover:bg-emerald-500">
          Connect with a driver account
        </ConnectLink>
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
