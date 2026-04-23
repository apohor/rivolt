import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Card, PageHeader } from "../components/ui";
import { LivePanel } from "../components/LivePanel";

export default function LivePage() {
  // Mirror LivePanel's own vehicles query so we can tell the user why
  // nothing is rendering when the client is the stub / not connected.
  // LivePanel itself just hides in that case, which is the right call
  // on the Overview but wrong on a dedicated page.
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    staleTime: 5 * 60_000,
  });
  const empty =
    !vehicles.isLoading &&
    !vehicles.isError &&
    (vehicles.data ?? []).length === 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Live"
        subtitle="Current vehicle state, refreshed every 30 s"
      />
      {empty ? (
        <Card title="No vehicles connected">
          <p className="text-sm text-neutral-400">
            Rivolt is running without a connected Rivian account
            (<code className="text-neutral-300">RIVIAN_CLIENT=stub</code>).
            Set <code className="text-neutral-300">RIVIAN_CLIENT=mock</code>{" "}
            for a demo vehicle, or{" "}
            <code className="text-neutral-300">RIVIAN_CLIENT=live</code> once
            the Settings login flow lands.
          </p>
        </Card>
      ) : (
        <LivePanel />
      )}
    </div>
  );
}
