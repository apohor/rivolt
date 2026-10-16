import { Link } from "react-router-dom";
import { useSignupEnabled } from "../lib/config";

// The sample-truck banner shown across the whole /demo shell.
export function DemoBanner() {
  // Sign-ups closed: "Connect with a driver account" goes to sign in instead of a
  // signup page that would only say it's closed. null (loading) behaves
  // as open.
  const signupEnabled = useSignupEnabled();
  const to = signupEnabled === false ? "/login" : "/signup";
  return (
    <div role="note" className="border-b border-sky-800/60 bg-sky-950/40 px-4 py-2 text-sm text-sky-100">
      <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-2">
        <span>
          <span className="font-medium">Sample truck.</span> Generated data, read-only. Connect your Rivian to see
          yours.
        </span>
        <Link to={to} className="rounded-md bg-emerald-600 px-3 py-1 text-xs font-semibold text-white hover:bg-emerald-500">
          Connect with a driver account
        </Link>
      </div>
    </div>
  );
}

// ConnectHint: the standard "this needs your own truck" line that
// replaces a mutating control in the demo.
export function ConnectHint({ what }: { what: string }) {
  const signupEnabled = useSignupEnabled();
  const to = signupEnabled === false ? "/login" : "/signup";
  return (
    <p className="text-xs text-neutral-500" data-testid="connect-hint">
      <Link to={to} className="text-emerald-300 hover:underline">
        Connect your Rivian
      </Link>{" "}
      to {what}.
    </p>
  );
}
