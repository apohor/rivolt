import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { backend, type GeocodeResult, type TripPlan, type TripRoute } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { TripRouteMap } from "../components/TripRouteMap";

// TX_PRESETS are city-hall lat/lon for one-click destination testing.
const TX_PRESETS: { label: string; lat: number; lon: number }[] = [
  { label: "Dallas", lat: 32.7767, lon: -96.797 },
  { label: "Houston", lat: 29.7604, lon: -95.3698 },
  { label: "San Antonio", lat: 29.4241, lon: -98.4936 },
  { label: "Big Bend NP", lat: 29.3267, lon: -103.207 },
];

// Selection is what the form actually plans against. lat/lon are
// resolved (search result, preset, or current vehicle position);
// label is human-readable so the form can render "From: Home" and
// not bother the user with raw coordinates.
type Selection = {
  lat: number;
  lon: number;
  label: string;
} | null;

type DriveMode =
  | ""
  | "EVERYDAY"
  | "DISTANCE"
  | "SPORT"
  | "WINTER"
  | "OFF_ROAD_AUTO";

export default function TripPlanPage() {
  const [origin, setOrigin] = useState<Selection>(null);
  const [destination, setDestination] = useState<Selection>(null);
  const [targetSoc, setTargetSoc] = useState<string>("20");
  const [driveMode, setDriveMode] = useState<DriveMode>("");
  const [hasAdapter, setHasAdapter] = useState<boolean>(false);

  const ownedQuery = useQuery({
    queryKey: ["vehicles", "owned"],
    queryFn: backend.listOwnedVehicles,
    staleTime: 5 * 60 * 1000,
  });
  const firstVehicle = ownedQuery.data?.vehicles?.[0];
  const stateQuery = useQuery({
    queryKey: ["vehicleState", firstVehicle?.rivian_vehicle_id],
    queryFn: () => backend.vehicleState(firstVehicle!.rivian_vehicle_id),
    enabled: !!firstVehicle?.rivian_vehicle_id,
    staleTime: 30 * 1000,
  });
  const stateLat = stateQuery.data?.latitude;
  const stateLon = stateQuery.data?.longitude;
  const stateHasFix =
    typeof stateLat === "number" &&
    typeof stateLon === "number" &&
    !(stateLat === 0 && stateLon === 0);

  // Fallback to the latest persisted sample when /api/state returns
  // (0, 0) — the WS frame may not have carried GNSSLocation. Walk
  // newest → oldest to pick the most recent fix.
  const samplesQuery = useQuery({
    queryKey: ["samplesLatestForPlanner"],
    queryFn: () =>
      backend.samples(new Date(Date.now() - 6 * 60 * 60 * 1000), 5000),
    enabled: stateQuery.isFetched && !stateHasFix,
    staleTime: 30 * 1000,
  });

  // currentPosition is whichever source has the freshest GPS fix:
  // /api/state if it's non-(0,0), else the newest sample with real
  // coords. null when neither is available.
  let currentPosition: { lat: number; lon: number } | null = null;
  if (stateHasFix) {
    currentPosition = { lat: stateLat as number, lon: stateLon as number };
  } else {
    const samples = samplesQuery.data ?? [];
    for (let i = samples.length - 1; i >= 0; i--) {
      const s = samples[i];
      if (s.Lat !== 0 || s.Lon !== 0) {
        currentPosition = { lat: s.Lat, lon: s.Lon };
        break;
      }
    }
  }

  // Pre-fill origin to current vehicle position once on mount when
  // the user hasn't already picked something. The user can still
  // override via search or a preset.
  useEffect(() => {
    if (origin) return;
    if (!currentPosition) return;
    setOrigin({
      lat: currentPosition.lat,
      lon: currentPosition.lon,
      label: "Current vehicle position",
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentPosition?.lat, currentPosition?.lon]);

  const homeQuery = useQuery({
    queryKey: ["settings", "homeLocation"],
    queryFn: backend.homeLocationGet,
    staleTime: 60 * 1000,
  });
  const home = homeQuery.data?.set ? homeQuery.data : null;

  // Pre-fill drive mode + adapter from saved planner prefs. The
  // user can still override per-trip; we don't write back unless
  // they hit "Save as default".
  const prefsQuery = useQuery({
    queryKey: ["settings", "plannerPrefs"],
    queryFn: backend.plannerPrefsGet,
    staleTime: 60 * 1000,
  });
  const [prefsApplied, setPrefsApplied] = useState(false);
  useEffect(() => {
    if (prefsApplied) return;
    if (!prefsQuery.data) return;
    if (prefsQuery.data.drive_mode) {
      setDriveMode(prefsQuery.data.drive_mode);
    }
    if (typeof prefsQuery.data.has_adapter === "boolean") {
      setHasAdapter(prefsQuery.data.has_adapter);
    }
    setPrefsApplied(true);
  }, [prefsQuery.data, prefsApplied]);

  const planMutation = useMutation({
    mutationFn: () => {
      if (!origin || !destination) {
        return Promise.reject(new Error("origin + destination required"));
      }
      const target = targetSoc.trim() === "" ? undefined : Number(targetSoc);
      const vid = firstVehicle?.rivian_vehicle_id;
      const soc = stateQuery.data?.battery_level_pct;
      return backend.planTrip({
        vehicle_id: vid,
        starting_soc: typeof soc === "number" && soc > 0 ? soc : undefined,
        origin_bearing: 0,
        target_arrival_soc_percent: target,
        drive_mode: driveMode || undefined,
        has_adapter: hasAdapter,
        waypoints: [
          { latitude: origin.lat, longitude: origin.lon, waypoint_type: "OTHER" },
          { latitude: destination.lat, longitude: destination.lon, waypoint_type: "OTHER" },
        ],
      });
    },
  });

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    planMutation.mutate();
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Trip planner"
        subtitle="Plan a route with charging stops via Rivian's planner."
      />

      <Card title="Plan a trip">
        {firstVehicle && (
          <p className="mb-3 text-xs text-neutral-500">
            Vehicle: <span className="font-mono">{firstVehicle.display_name || firstVehicle.rivian_vehicle_id}</span>
            {typeof stateQuery.data?.battery_level_pct === "number" && (
              <> · SoC: <span className="font-mono">{stateQuery.data.battery_level_pct.toFixed(0)}%</span></>
            )}
          </p>
        )}
        <form onSubmit={onSubmit} className="space-y-4">
          <LocationField
            heading="From"
            value={origin}
            onChange={setOrigin}
            presets={[
              ...(currentPosition
                ? [{
                    label: "Current vehicle position",
                    lat: currentPosition.lat,
                    lon: currentPosition.lon,
                  }]
                : []),
              ...(home
                ? [{ label: home.label || "Home", lat: home.latitude, lon: home.longitude }]
                : []),
            ]}
          />
          <LocationField
            heading="To"
            value={destination}
            onChange={setDestination}
            presets={[
              ...(home
                ? [{ label: home.label || "Home", lat: home.latitude, lon: home.longitude }]
                : []),
              ...TX_PRESETS,
            ]}
          />
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <label className="flex flex-col gap-1 text-sm">
              <span className="text-neutral-400">Target arrival SoC %</span>
              <input
                type="number"
                min={0}
                max={100}
                value={targetSoc}
                onChange={(e) => setTargetSoc(e.target.value)}
                className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              <span className="text-neutral-400">Drive mode</span>
              <select
                value={driveMode}
                onChange={(e) => setDriveMode(e.target.value as DriveMode)}
                className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
              >
                <option value="">Default (let planner pick)</option>
                <option value="EVERYDAY">All-Purpose</option>
                <option value="DISTANCE">Conserve (fewer / shorter stops)</option>
                <option value="SPORT">Sport</option>
                <option value="WINTER">Snow</option>
                <option value="OFF_ROAD_AUTO">All-Terrain</option>
              </select>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              <span className="text-neutral-400">Tesla NACS adapter</span>
              <label className="flex items-center gap-2 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2">
                <input
                  type="checkbox"
                  checked={hasAdapter}
                  onChange={(e) => setHasAdapter(e.target.checked)}
                  className="h-4 w-4 accent-emerald-600"
                />
                <span className="text-neutral-300">
                  Vehicle has the Tesla adapter (allows Supercharger stops)
                </span>
              </label>
            </label>
          </div>
          <div className="flex items-center gap-3">
            <button
              type="submit"
              disabled={planMutation.isPending || !origin || !destination}
              className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-medium text-emerald-50 hover:bg-emerald-600 disabled:cursor-not-allowed disabled:bg-neutral-800 disabled:text-neutral-500"
            >
              Plan trip
            </button>
            {planMutation.isPending && <Spinner />}
            <span className="text-xs text-neutral-500">
              Set Home in <a href="/settings" className="underline hover:text-neutral-300">Settings</a> for one-click presets.
            </span>
          </div>
        </form>
      </Card>

      {planMutation.error && (
        <ErrorBox
          title="Planner failed"
          detail={(planMutation.error as Error).message}
        />
      )}

      {planMutation.data && <TripPlanResult plan={planMutation.data} />}
    </div>
  );
}

// LocationField is a single Origin or Destination row: shows the
// current selection's label, a typeahead search, and preset
// buttons. No raw lat/lon visible.
function LocationField({
  heading,
  value,
  onChange,
  presets,
}: {
  heading: string;
  value: Selection;
  onChange: (s: Selection) => void;
  presets: { label: string; lat: number; lon: number }[];
}) {
  return (
    <div className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3">
      <div className="flex items-baseline justify-between gap-3">
        <div className="text-sm">
          <span className="text-neutral-400">{heading}: </span>
          <span className="text-neutral-100">
            {value ? value.label : <span className="text-neutral-500">— pick a place —</span>}
          </span>
        </div>
        {value && (
          <button
            type="button"
            onClick={() => onChange(null)}
            className="text-xs text-neutral-500 hover:text-neutral-300"
          >
            clear
          </button>
        )}
      </div>
      <div className="mt-3">
        <LocationSearch
          placeholder="Type a city — Austin, Dallas, Big Bend…"
          onSelect={(r) =>
            onChange({
              lat: r.latitude,
              lon: r.longitude,
              label: formatGeocode(r),
            })
          }
        />
      </div>
      {presets.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-2 text-xs">
          {presets.map((p) => (
            <button
              key={p.label}
              type="button"
              onClick={() => onChange({ lat: p.lat, lon: p.lon, label: p.label })}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 hover:border-neutral-500 hover:bg-neutral-800"
            >
              {p.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// LocationSearch is a debounced typeahead over /api/geocode. Emits
// the selected GeocodeResult upward; the parent decides what to do
// with it. Internal text state is kept here so the parent doesn't
// have to wire it; clearing happens on selection.
function LocationSearch({
  placeholder,
  onSelect,
}: {
  placeholder: string;
  onSelect: (r: GeocodeResult) => void;
}) {
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const t = setTimeout(() => setDebounced(query), 300);
    return () => clearTimeout(t);
  }, [query]);

  const results = useQuery({
    queryKey: ["geocode", debounced],
    queryFn: () => backend.geocode(debounced, 5),
    enabled: debounced.trim().length >= 2,
    staleTime: 5 * 60 * 1000,
  });

  useEffect(() => {
    if (!open) return;
    const onDocClick = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  }, [open]);

  const items = results.data ?? [];

  return (
    <div className="relative" ref={containerRef}>
      <input
        type="text"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        placeholder={placeholder}
        className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
      />
      {open && items.length > 0 && (
        <ul className="absolute left-0 right-0 top-[calc(100%+2px)] z-10 max-h-64 overflow-y-auto rounded-md border border-neutral-700 bg-neutral-900 shadow-lg">
          {items.map((r) => (
            <li key={`${r.latitude},${r.longitude}`}>
              <button
                type="button"
                onClick={() => {
                  onSelect(r);
                  setQuery("");
                  setOpen(false);
                }}
                className="block w-full px-3 py-2 text-left hover:bg-neutral-800"
              >
                <div className="text-neutral-100">{r.name}</div>
                <div className="text-xs text-neutral-500">
                  {[r.admin1, r.country].filter(Boolean).join(", ")}
                  {typeof r.population === "number" && r.population > 0 && (
                    <> · pop. {r.population.toLocaleString()}</>
                  )}
                </div>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function formatGeocode(r: GeocodeResult): string {
  return [r.name, r.admin1, r.country].filter(Boolean).join(", ");
}

function TripPlanResult({ plan }: { plan: TripPlan }) {
  if (plan.Routes.length === 0) {
    return (
      <Card title="Result">
        <p className="text-sm text-neutral-400">
          Status: <span className="font-mono">{plan.Status || "—"}</span>. No
          routes returned. {plan.SoCBelowLimit && "(SoC below the configured limit.)"}
          {!plan.ChargeStationsAvailable && " (No reachable charging stations along the corridor.)"}
        </p>
      </Card>
    );
  }
  return (
    <div className="space-y-4">
      {plan.Routes.map((route, i) => (
        <RouteCard key={i} route={route} index={i} />
      ))}
      {plan.SoCBelowLimit && (
        <ErrorBox
          title="SoC below limit"
          detail="Rivian flagged this plan as crossing the configured low-SoC threshold."
        />
      )}
    </div>
  );
}

function RouteCard({ route, index }: { route: TripRoute; index: number }) {
  const charging = route.Waypoints.filter(
    (w) => w.WaypointType !== "ORIGIN" && w.WaypointType !== "DESTINATION" && w.WaypointType !== "OTHER",
  );
  const totalChargeMin = Math.round(route.TotalChargingDurationSec / 60);
  return (
    <Card title={`Route ${index + 1}${route.DestinationReached ? "" : " — destination unreachable"}`}>
      <div className="mb-4">
        <TripRouteMap route={route} />
      </div>
      <dl className="grid grid-cols-2 gap-y-2 gap-x-6 text-sm sm:grid-cols-4">
        <Stat label="Charging stops" value={String(charging.length)} />
        <Stat label="Total charge time" value={totalChargeMin > 0 ? `${totalChargeMin} min` : "—"} />
        <Stat label="Arrival SoC" value={route.ArrivalSoC > 0 ? `${route.ArrivalSoC.toFixed(0)}%` : "—"} />
        <Stat
          label="Energy used"
          value={route.EnergyConsumptionKWh > 0 ? `${route.EnergyConsumptionKWh.toFixed(1)} kWh` : "—"}
        />
      </dl>
      {charging.length > 0 && (
        <div className="mt-4 overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-neutral-800 text-left text-xs uppercase tracking-wide text-neutral-500">
              <tr>
                <th className="px-2 py-2">#</th>
                <th className="px-2 py-2">Stop</th>
                <th className="px-2 py-2">Arrive</th>
                <th className="px-2 py-2">Depart</th>
                <th className="px-2 py-2">Charge</th>
                <th className="px-2 py-2">Max kW</th>
                <th className="px-2 py-2">Adapter?</th>
              </tr>
            </thead>
            <tbody>
              {charging.map((w, j) => (
                <tr key={j} className="border-b border-neutral-900">
                  <td className="px-2 py-2 text-neutral-500">{j + 1}</td>
                  <td className="px-2 py-2">{w.Name || `(${w.Latitude.toFixed(3)}, ${w.Longitude.toFixed(3)})`}</td>
                  <td className="px-2 py-2 font-mono">{w.ArrivalSoC.toFixed(0)}%</td>
                  <td className="px-2 py-2 font-mono">{w.DepartureSoC.toFixed(0)}%</td>
                  <td className="px-2 py-2 font-mono">{Math.round(w.ChargeDurationSec / 60)} min</td>
                  <td className="px-2 py-2 font-mono">{w.MaxPowerKW > 0 ? w.MaxPowerKW.toFixed(0) : "—"}</td>
                  <td className="px-2 py-2">{w.AdapterRequired ? "yes" : ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {!route.DestinationReached && route.BatteryEmptyToDestMeters > 0 && (
        <p className="mt-3 text-sm text-amber-400">
          Battery empty {(route.BatteryEmptyToDestMeters / 1000).toFixed(1)} km short of destination
          {route.BatteryEmptyLat !== 0 && (
            <>
              {" "}at ({route.BatteryEmptyLat.toFixed(3)}, {route.BatteryEmptyLon.toFixed(3)})
            </>
          )}
          .
        </p>
      )}
    </Card>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-neutral-500">{label}</dt>
      <dd className="font-mono text-neutral-100">{value}</dd>
    </div>
  );
}
