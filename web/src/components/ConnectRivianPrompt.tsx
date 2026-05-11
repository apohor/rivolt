import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { Card } from "./ui";

// ConnectRivianPrompt is a top-of-page CTA shown when the user has
// no live Rivian session, on every page that's empty without one
// (Overview, Drives, Charges, Plan). Replaces the "blank dashboard
// looks broken" first-time-user state.
//
// Self-contained: it owns its own rivianStatus query (cheap, shared
// with the layout's StatusPill via react-query's cache so we don't
// re-hit the endpoint).
export default function ConnectRivianPrompt({
  context,
}: {
  // Page-specific phrase appended to the headline. Keep it short.
  context?: string;
}) {
  const status = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    staleTime: 30_000,
  });
  if (status.isLoading || !status.data) return null;
  if (status.data.authenticated) return null;
  return (
    <Card>
      <div className="flex flex-wrap items-center gap-4">
        <div className="flex-1 min-w-[200px]">
          <h2 className="text-base font-semibold text-neutral-100">
            Connect your Rivian to get started
          </h2>
          <p className="mt-1 text-sm text-neutral-400">
            Rivolt needs to sign in to Rivian's servers to pull
            telemetry, drives, and charging history.{" "}
            {context && (
              <span className="text-neutral-500">{context}</span>
            )}
          </p>
        </div>
        <Link
          to="/settings?tab=account#rivian"
          className="shrink-0 rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500"
        >
          Connect Rivian
        </Link>
      </div>
    </Card>
  );
}
