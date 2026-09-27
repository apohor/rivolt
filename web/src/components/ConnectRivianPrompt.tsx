import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { AuthorizedDriverNote, RivianAccountPanel } from "./RivianAccountPanel";
import { Card } from "./ui";

// ConnectRivianPrompt is a top-of-page CTA shown on every page that's
// empty until Rivolt is recording a vehicle (Overview, Drives, Charges,
// Plan). Replaces the "blank dashboard looks broken" first-time-user
// state. Two cases:
//
//   - No Rivian session: connect.
//   - Connected, but the account has no vehicles: the sign-in worked
//     yet nothing will ever record (usually an unaccepted Authorized
//     Driver invite). Without this the page just stayed blank.
//
// `inline` renders the sign-in form right in the card instead of
// linking to Settings. The Overview uses it: it's where skipped
// onboarding and the finish-setup emails land, and every extra hop
// between "I'm ready" and the password field loses people.
//
// Self-contained: it owns its own rivianStatus query (cheap, shared
// with the layout's StatusPill via react-query's cache so we don't
// re-hit the endpoint).
export default function ConnectRivianPrompt({
  context,
  inline = false,
}: {
  // Page-specific phrase appended to the headline. Keep it short.
  context?: string;
  inline?: boolean;
}) {
  const status = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    staleTime: 30_000,
  });
  const authenticated = !!status.data?.authenticated;
  const owned = useQuery({
    queryKey: ["vehicles", "owned"],
    queryFn: () => backend.listOwnedVehicles(),
    enabled: authenticated,
  });
  if (status.isLoading || !status.data || !status.data.enabled) return null;
  const noVehicles =
    authenticated && !!owned.data && owned.data.vehicles.length === 0;
  if (authenticated && !noVehicles) return null;

  const headline = noVehicles
    ? "Connected, but no vehicles found"
    : "Connect your Rivian to get started";
  const blurb = noVehicles
    ? "Your Rivian sign-in worked, but that account doesn't list a vehicle yet, so there's nothing to record."
    : "Sign in with the same email and password you use in the Rivian app — Rivolt only reads your vehicle data.";

  if (inline) {
    return (
      <Card>
        {/* When connected with no vehicles, the panel's own warning
            (with "Check again") says it all - no second headline. */}
        <div className="max-w-md space-y-3">
          {!noVehicles && (
            <div>
              <h2 className="text-base font-semibold text-neutral-100">
                {headline}
              </h2>
              <p className="mt-1 text-sm text-neutral-400">
                {blurb}{" "}
                {context && (
                  <span className="text-neutral-500">{context}</span>
                )}
              </p>
            </div>
          )}
          <RivianAccountPanel />
          {!noVehicles && <AuthorizedDriverNote />}
        </div>
      </Card>
    );
  }

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-4">
        <div className="flex-1 min-w-[200px]">
          <h2 className="text-base font-semibold text-neutral-100">
            {headline}
          </h2>
          <p className="mt-1 text-sm text-neutral-400">
            {blurb}{" "}
            {context && !noVehicles && (
              <span className="text-neutral-500">{context}</span>
            )}
          </p>
        </div>
        <Link
          to="/settings?tab=account#rivian"
          className="shrink-0 rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500"
        >
          {noVehicles ? "Fix in Settings" : "Connect Rivian"}
        </Link>
      </div>
    </Card>
  );
}
