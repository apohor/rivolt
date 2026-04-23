import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { Card, PageHeader, Spinner } from "../components/ui";
import { LivePanel } from "../components/LivePanel";

export default function LivePage() {
  // Reflect the Rivian sign-in state on this page explicitly: the
  // dedicated Live page shouldn't silently render blank the way the
  // Overview summary does.
  const status = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    staleTime: 30_000,
  });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Live"
        subtitle="Current vehicle state, refreshed every 30 s"
      />
      {status.isLoading ? (
        <Spinner />
      ) : !status.data?.enabled ? (
        <Card title="Live client disabled">
          <p className="text-sm text-neutral-400">
            The Rivian live client is off (
            <code className="text-neutral-300">RIVIAN_CLIENT=stub</code>). Set{" "}
            <code className="text-neutral-300">RIVIAN_CLIENT=mock</code> for
            fixture data, or leave it unset and sign in from{" "}
            <Link to="/settings" className="text-emerald-400 hover:underline">
              Settings
            </Link>
            .
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
