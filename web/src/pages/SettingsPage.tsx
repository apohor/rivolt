import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";

export default function SettingsPage() {
  const health = useQuery({ queryKey: ["health"], queryFn: () => backend.health() });
  const vehicles = useQuery({ queryKey: ["vehicles"], queryFn: () => backend.vehicles() });

  return (
    <div className="space-y-4">
      <PageHeader title="Settings" />

      <Card title="Backend">
        {health.isLoading ? (
          <Spinner />
        ) : health.isError ? (
          <ErrorBox title="Backend unreachable" detail={String(health.error)} />
        ) : (
          <dl className="text-sm grid grid-cols-[auto,1fr] gap-x-4 gap-y-1">
            <dt className="text-neutral-500">Version</dt>
            <dd className="text-neutral-200">{health.data?.version}</dd>
            <dt className="text-neutral-500">Server time</dt>
            <dd className="text-neutral-200">{health.data?.time}</dd>
          </dl>
        )}
      </Card>

      <Card title="Vehicles">
        {vehicles.isLoading ? (
          <Spinner />
        ) : vehicles.isError ? (
          <ErrorBox title="Failed to load vehicles" detail={String(vehicles.error)} />
        ) : !vehicles.data || vehicles.data.length === 0 ? (
          <div className="text-sm text-neutral-400">
            <p>No vehicles connected yet.</p>
            <p className="mt-1 text-xs text-neutral-500">
              Rivian account linking is not yet implemented. In the meantime, import
              historical data with <code>rivolt import electrafi &lt;file.csv&gt;</code>.
            </p>
          </div>
        ) : (
          <ul className="text-sm divide-y divide-neutral-800">
            {vehicles.data.map((v) => (
              <li key={v.id} className="py-2 flex justify-between">
                <span className="text-neutral-200">{v.name || v.vin}</span>
                <span className="text-neutral-500">{v.model}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card title="Notifications">
        <p className="text-sm text-neutral-400">
          Push notifications (charging complete, plug-in reminders, anomaly alerts)
          will land once the Rivian ingester is wired. The server-side VAPID keypair
          is already generated and persisted.
        </p>
      </Card>
    </div>
  );
}
