import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Card, PageHeader } from "../components/ui";
import { ConnectHint } from "../components/DemoBanner";
import { formatMoney } from "../lib/ledger";
import { AppearanceCard } from "./SettingsPage";
import { setDistanceUnit, setTemperatureUnit, usePreferences, type DistanceUnit, type TemperatureUnit } from "../lib/preferences";

// Settings in the public demo: read-only. Units and appearance are local
// to this device, so they work; everything that would write (home
// location, rates, vehicle profile, notifications, data) is shown as the
// sample truck's values or hidden behind "Connect your Rivian".

function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
}) {
  return (
    <div>
      <div className="mb-1 text-neutral-400">{label}</div>
      <div role="radiogroup" aria-label={label} className="inline-flex rounded-md border border-neutral-700 overflow-hidden">
        {options.map((o) => (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={value === o.value}
            onClick={() => onChange(o.value)}
            className={`px-3 py-1.5 text-xs transition-colors ${
              value === o.value ? "bg-emerald-600/20 text-emerald-300" : "text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800"
            }`}
          >
            {o.label}
          </button>
        ))}
      </div>
    </div>
  );
}

export default function DemoSettings() {
  const { temperatureUnit, distanceUnit } = usePreferences();
  const charging = useQuery({ queryKey: ["settings", "charging"], queryFn: () => backend.getChargingSettings(), retry: 0 });
  const networks = useQuery({ queryKey: ["charging-networks"], queryFn: () => backend.getChargingNetworks(), retry: 0 });
  const home = useQuery({ queryKey: ["settings", "homeLocation"], queryFn: () => backend.homeLocationGet(), retry: 0 });
  const c = charging.data;
  return (
    <div className="space-y-4">
      <PageHeader title="Settings" subtitle="Sample truck, read-only" />

      <AppearanceCard />

      <Card title="Units">
        <div className="space-y-4 text-sm">
          <Segmented<DistanceUnit>
            label="Distance"
            value={distanceUnit}
            options={[
              { value: "mi", label: "Miles (mi)" },
              { value: "km", label: "Kilometers (km)" },
            ]}
            onChange={setDistanceUnit}
          />
          <Segmented<TemperatureUnit>
            label="Temperature"
            value={temperatureUnit}
            options={[
              { value: "c", label: "Celsius (°C)" },
              { value: "f", label: "Fahrenheit (°F)" },
            ]}
            onChange={setTemperatureUnit}
          />
          <p className="text-xs text-neutral-500">Saved on this device only.</p>
        </div>
      </Card>

      <Card title="Charging rates">
        {c ? (
          <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
            <dt className="text-neutral-500">Home rate</dt>
            <dd className="text-right tabular-nums text-neutral-200">
              {formatMoney(c.home_price_per_kwh, c.home_currency || "USD", 3)}/kWh
            </dd>
            {home.data?.set && (
              <>
                <dt className="text-neutral-500">Home location</dt>
                <dd className="text-right text-neutral-200">{home.data.label || "Home"}</dd>
              </>
            )}
            {(networks.data ?? []).map((n) => (
              <div key={n.name} className="contents">
                <dt className="text-neutral-500">{n.name}</dt>
                <dd className="text-right tabular-nums text-neutral-200">
                  {formatMoney(n.price_per_kwh, n.currency || "USD", 3)}/kWh
                  {(n.member_price_per_kwh ?? 0) > 0 && (
                    <span className="ml-1 text-xs text-neutral-500">
                      member {formatMoney(n.member_price_per_kwh!, n.currency || "USD", 3)}
                    </span>
                  )}
                </dd>
              </div>
            ))}
          </dl>
        ) : (
          <p className="text-xs text-neutral-500">Loading…</p>
        )}
        <div className="mt-3">
          <ConnectHint what="set your own rates, home location and memberships" />
        </div>
      </Card>

      <Card title="Account, notifications and data">
        <ConnectHint what="manage your Rivian connection, notifications, exports and account" />
      </Card>
    </div>
  );
}
