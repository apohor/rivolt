import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { Card, PageHeader, Spinner } from "../components/ui";
import { LivePanel } from "../components/LivePanel";
import { useImpersonationTarget } from "../lib/impersonation";

export default function LivePage() {
  // Reflect the Rivian sign-in state on this page explicitly: the
  // dedicated Live page shouldn't silently render blank the way the
  // Overview summary does.
  const status = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    staleTime: 30_000,
  });
  const impersonating = useImpersonationTarget();

  return (
    <div className="space-y-6">
      <Link
        to="/"
        data-testid="live-back"
        className="-ml-2 inline-flex min-h-11 items-center gap-1 rounded-lg px-2 text-sm text-neutral-300 hover:text-emerald-300 pointer-fine:min-h-8"
      >
        <span aria-hidden="true">←</span> Now
      </Link>
      <PageHeader
        title="Live"
        subtitle="Streaming vehicle state from Rivian"
      />
      {impersonating ? (
        <Card title="Live view unavailable while impersonating">
          <p className="text-sm text-neutral-400">
            v1 limitation: real-time updates depend on a WebSocket
            subscription that can't carry the impersonation header, so
            Live is hidden entirely while viewing as{" "}
            <strong>{impersonating.email}</strong> rather than risk
            showing your own vehicle's data instead of theirs. Exit the
            impersonation banner above to use Live again.
          </p>
        </Card>
      ) : status.isLoading ? (
        <Spinner />
      ) : !status.data?.enabled ? (
        <Card title="Live isn't available">
          <p className="text-sm text-neutral-400">
            Rivian isn't available right now.{" "}
            <button type="button" onClick={() => void status.refetch()} className="text-emerald-400 hover:underline">
              Try again
            </button>
          </p>
        </Card>
      ) : !status.data.authenticated ? (
        <Card title="Not connected">
          <p className="text-sm text-neutral-400">
            Sign in to your Rivian account to see live state.{" "}
            <Link to="/settings" className="text-emerald-400 hover:underline">
              Open Settings →
            </Link>
          </p>
        </Card>
      ) : (
        <LivePanel />
      )}
    </div>
  );
}
