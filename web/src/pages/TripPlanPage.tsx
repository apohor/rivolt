import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { backend, type GeocodeResult, type TripAdvice, type TripPlan, type TripRoute } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import ConnectRivianPrompt from "../components/ConnectRivianPrompt";
import { useAIEnabled } from "../lib/config";

// Lazy-load TripRouteMap so the Leaflet + Protomaps + protomaps-leaflet
// bundle (several hundred KB) only ships when the user actually opens
// a plan with a rendered map, not on every page navigation.
const TripRouteMap = lazy(() =>
  import("../components/TripRouteMap").then((m) => ({ default: m.TripRouteMap })),
);

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

// LAST_TRIP_KEY versions the localStorage shape. Bump on breaking
// changes to the persisted struct so a stale entry from an older
// build can't crash the page on rehydrate.
const LAST_TRIP_KEY = "rivolt:trip-planner:last-trip:v1";

// HOME_LABEL_RADIUS_M is how close a selected point must be to the
// saved home location before the planner relabels it. 150 m covers
// driveway-vs-street-address geocode jitter without false-positiving
// onto a neighbor's lot.
const HOME_LABEL_RADIUS_M = 150;

function haversineMeters(
  a: { lat: number; lon: number },
  b: { lat: number; lon: number },
): number {
  const R = 6371000;
  const toRad = (x: number) => (x * Math.PI) / 180;
  const dLat = toRad(b.lat - a.lat);
  const dLon = toRad(b.lon - a.lon);
  const sLat = Math.sin(dLat / 2);
  const sLon = Math.sin(dLon / 2);
  const h =
    sLat * sLat +
    Math.cos(toRad(a.lat)) * Math.cos(toRad(b.lat)) * sLon * sLon;
  return 2 * R * Math.asin(Math.sqrt(h));
}

type HomePoint = { latitude: number; longitude: number; label?: string };

function relabelIfHome<T extends { lat: number; lon: number; label: string }>(
  sel: T,
  home: HomePoint | null,
): T {
  if (!home) return sel;
  const d = haversineMeters(
    { lat: sel.lat, lon: sel.lon },
    { lat: home.latitude, lon: home.longitude },
  );
  if (d > HOME_LABEL_RADIUS_M) return sel;
  return { ...sel, label: home.label || "Home" };
}

type LastTrip = {
  origin: NonNullable<Selection>;
  destination: NonNullable<Selection>;
  extraStops: NonNullable<Selection>[];
  targetSoc: string;
  driveMode: DriveMode;
  hasAdapter: boolean;
};

function readLastTrip(): LastTrip | null {
  try {
    const raw = localStorage.getItem(LAST_TRIP_KEY);
    if (!raw) {
      console.debug("[trip-planner] no saved trip in localStorage");
      return null;
    }
    const parsed = JSON.parse(raw) as LastTrip;
    console.debug("[trip-planner] hydrated last trip:", parsed);
    return parsed;
  } catch (e) {
    console.warn("[trip-planner] readLastTrip failed:", e);
    return null;
  }
}

function writeLastTrip(t: LastTrip): void {
  try {
    localStorage.setItem(LAST_TRIP_KEY, JSON.stringify(t));
    console.debug("[trip-planner] saved last trip:", t);
  } catch (e) {
    console.warn("[trip-planner] writeLastTrip failed:", e);
  }
}

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
  const [extraStops, setExtraStops] = useState<NonNullable<Selection>[]>([]);
  const [targetSoc, setTargetSoc] = useState<string>("20");
  // Empty = auto from live vehicle state; user can override.
  const [startingSoc, setStartingSoc] = useState<string>("");
  const [driveMode, setDriveMode] = useState<DriveMode>("");
  const [hasAdapter, setHasAdapter] = useState<boolean>(false);
  // departureAt is a datetime-local string (local time, no timezone).
  // Empty = depart now; Rivian's planner always plans from "now", so
  // we store this and shift displayed waypoint times client-side.
  const [departureAt, setDepartureAt] = useState<string>("");
  const aiEnabled = useAIEnabled();

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
  // Per-vehicle profile carries the user-configured door-jamb
  // placard PSI; surfaced into the trip-advice prompt so the
  // "vehicle" section uses real numbers (not a hardcoded 35 PSI).
  const profileQuery = useQuery({
    queryKey: ["vehicleProfile", firstVehicle?.rivian_vehicle_id],
    queryFn: () => backend.vehicleProfileGet(firstVehicle!.rivian_vehicle_id),
    enabled: !!firstVehicle?.rivian_vehicle_id,
    staleTime: 5 * 60_000,
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

  const homeQuery = useQuery({
    queryKey: ["settings", "homeLocation"],
    queryFn: backend.homeLocationGet,
    staleTime: 60 * 1000,
  });
  const home = homeQuery.data?.set ? homeQuery.data : null;

  // Hydrate from the last successful plan on mount. Runs before the
  // current-position prefill below so a saved trip wins; the user
  // can clear / change any field from there. Skipped silently when
  // there's no entry or the JSON is malformed. Saved selections are
  // re-passed through relabelIfHome so a trip persisted before the
  // user set their home location upgrades to the "Home" label on
  // re-open without forcing a re-plan.
  const [lastTripApplied, setLastTripApplied] = useState(false);
  useEffect(() => {
    if (lastTripApplied) return;
    if (homeQuery.isLoading) return;
    const last = readLastTrip();
    setLastTripApplied(true);
    if (!last) return;
    setOrigin(relabelIfHome(last.origin, home));
    setDestination(relabelIfHome(last.destination, home));
    setExtraStops((last.extraStops ?? []).map((s) => relabelIfHome(s, home)));
    if (last.targetSoc) setTargetSoc(last.targetSoc);
    if (last.driveMode) setDriveMode(last.driveMode);
    if (typeof last.hasAdapter === "boolean") setHasAdapter(last.hasAdapter);
  }, [lastTripApplied, homeQuery.isLoading, home]);

  // Pre-fill origin to current vehicle position once on mount when
  // the user hasn't already picked something. Skipped when the
  // last-trip hydrator above already supplied an origin. If the
  // vehicle is parked at home, label the prefill as "Home" instead
  // of the generic "Current vehicle position".
  useEffect(() => {
    if (origin) return;
    if (!currentPosition) return;
    setOrigin(
      relabelIfHome(
        {
          lat: currentPosition.lat,
          lon: currentPosition.lon,
          label: "Current vehicle position",
        },
        home,
      ),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentPosition?.lat, currentPosition?.lon, home]);

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
      const liveSoc = stateQuery.data?.battery_level_pct;
      const manualSoc = startingSoc.trim() !== "" ? Number(startingSoc) : undefined;
      const soc = manualSoc ?? (typeof liveSoc === "number" && liveSoc > 0 ? liveSoc : undefined);
      return backend.planTrip({
        vehicle_id: vid,
        starting_soc: soc,
        origin_bearing: 0,
        target_arrival_soc_percent: target,
        drive_mode: driveMode || undefined,
        has_adapter: hasAdapter,
        waypoints: [
          { latitude: origin.lat, longitude: origin.lon, waypoint_type: "OTHER" },
          ...extraStops.map((s) => ({
            latitude: s.lat,
            longitude: s.lon,
            waypoint_type: "OTHER",
          })),
          { latitude: destination.lat, longitude: destination.lon, waypoint_type: "OTHER" },
        ],
      });
    },
    onSuccess: (plan) => {
      adviceMutation.reset();
      // Persist the inputs that produced this plan so reopening the
      // page restores the same starting state.
      if (origin && destination) {
        writeLastTrip({
          origin,
          destination,
          extraStops,
          targetSoc,
          driveMode,
          hasAdapter,
        });
      }
      // Auto-fire AI analysis on non-trivial trips so the user
      // doesn't need an extra tap on the road-trip path. "Non-trivial"
      // = >200 mi total drive OR at least one planner-picked charging
      // stop. Small commutes still wait for the explicit Analyze
      // button. Skipped entirely when no AI provider is configured.
      const r = plan.Routes[0];
      if (aiEnabled && r) {
        const miles = r.TotalDriveDistanceMeters / 1609.344;
        const hasStops = r.Waypoints.some((w) => {
          const t = w.WaypointType.toLowerCase();
          return t !== "origin" && t !== "destination" && t !== "waypoint" && t !== "other";
        });
        if (miles > 200 || hasStops) {
          fireAdvice(plan);
        }
      }
    },
  });

  const adviceMutation = useMutation({
    mutationFn: backend.planTripAdvice,
  });

  // fireAdvice packages everything the advice handler needs to build
  // the LLM prompt. Reused by the auto-fire path on plan success AND
  // the explicit Analyze button on the result card.
  const fireAdvice = (plan: TripPlan) => {
    if (!origin || !destination) return;
    const sd = stateQuery.data;
    adviceMutation.mutate({
      plan,
      origin: origin.label,
      destination: destination.label,
      drive_mode: driveMode || undefined,
      starting_soc: (startingSoc.trim() !== "" ? Number(startingSoc) : undefined) ?? sd?.battery_level_pct,
      has_adapter: hasAdapter,
      tire_fl_bar: typeof sd?.tire_pressure_fl_bar === "number" && sd.tire_pressure_fl_bar > 0 ? sd.tire_pressure_fl_bar : undefined,
      tire_fr_bar: typeof sd?.tire_pressure_fr_bar === "number" && sd.tire_pressure_fr_bar > 0 ? sd.tire_pressure_fr_bar : undefined,
      tire_rl_bar: typeof sd?.tire_pressure_rl_bar === "number" && sd.tire_pressure_rl_bar > 0 ? sd.tire_pressure_rl_bar : undefined,
      tire_rr_bar: typeof sd?.tire_pressure_rr_bar === "number" && sd.tire_pressure_rr_bar > 0 ? sd.tire_pressure_rr_bar : undefined,
      pack_kwh: typeof firstVehicle?.pack_kwh === "number" && firstVehicle.pack_kwh > 0 ? firstVehicle.pack_kwh : undefined,
      tire_placard_psi:
        typeof profileQuery.data?.tire_placard_psi === "number" &&
        profileQuery.data.tire_placard_psi > 0
          ? profileQuery.data.tire_placard_psi
          : undefined,
      departure_datetime: departureAt ? new Date(departureAt).toISOString() : undefined,
    });
  };

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    adviceMutation.reset();
    planMutation.mutate();
  };

  return (
    <div className="space-y-6">
      <ConnectRivianPrompt context="The planner uses your truck's pack, current SoC, and adapter config — connect first for accurate routes." />
      <PageHeader
        title="Trip planner"
        subtitle="Plan a route with charging stops via Rivian's planner."
      />

      <Card title="Plan a trip">
        {firstVehicle && (
          <p className="mb-3 text-xs text-neutral-500">
            Vehicle: <span className="font-mono">{firstVehicle.display_name || firstVehicle.rivian_vehicle_id}</span>
          </p>
        )}
        <form onSubmit={onSubmit} className="space-y-4">
          <DeparturePicker value={departureAt} onChange={setDepartureAt} />
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-4">
            <label className="flex flex-col gap-1 text-sm">
              <span className="text-neutral-400">Starting SoC %</span>
              <input
                type="number"
                min={1}
                max={100}
                value={startingSoc}
                onChange={(e) => setStartingSoc(e.target.value)}
                placeholder={
                  typeof stateQuery.data?.battery_level_pct === "number"
                    ? String(Math.round(stateQuery.data.battery_level_pct))
                    : "auto"
                }
                className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 placeholder-neutral-600 focus:border-neutral-500 focus:outline-none"
              />
            </label>
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
                <option value="DISTANCE">Conserve</option>
                <option value="SPORT">Sport</option>
                <option value="WINTER">Snow</option>
                <option value="OFF_ROAD_AUTO">All-Terrain</option>
              </select>
            </label>
            <div className="flex flex-col gap-1 text-sm">
              <span className="text-neutral-400">Tesla NACS adapter</span>
              <button
                type="button"
                role="switch"
                aria-checked={hasAdapter}
                onClick={() => setHasAdapter((v) => !v)}
                className={`flex items-center justify-between rounded-md border border-neutral-700 px-3 py-2 transition-colors ${
                  hasAdapter ? "bg-emerald-900/30" : "bg-neutral-900"
                }`}
              >
                <span className="text-neutral-300">{hasAdapter ? "Yes" : "No"}</span>
                <span
                  className={`inline-flex h-5 w-9 items-center rounded-full px-0.5 transition-colors ${
                    hasAdapter ? "bg-emerald-600 justify-end" : "bg-neutral-700 justify-start"
                  }`}
                >
                  <span className="inline-block h-4 w-4 rounded-full bg-white shadow" />
                </span>
              </button>
            </div>
          </div>
          <LocationField
            heading="From"
            value={origin}
            onChange={(s) => setOrigin(s ? relabelIfHome(s, home) : null)}
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
          <ViaStopList
            stops={extraStops}
            onRemove={(i) => setExtraStops((prev) => prev.filter((_, j) => j !== i))
            }
            onAdd={(stop) => {
              const labeled = relabelIfHome(stop, home);
              setExtraStops((prev) =>
                prev.some(
                  (s) =>
                    Math.abs(s.lat - labeled.lat) < 0.0005 &&
                    Math.abs(s.lon - labeled.lon) < 0.0005,
                )
                  ? prev
                  : [...prev, labeled],
              );
            }}
          />
          <LocationField
            heading="To"
            value={destination}
            onChange={(s) => setDestination(s ? relabelIfHome(s, home) : null)}
            presets={[
              ...(home
                ? [{ label: home.label || "Home", lat: home.latitude, lon: home.longitude }]
                : []),
              ...TX_PRESETS,
            ]}
          />
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

      {planMutation.data && (
        <TripPlanResult
          plan={planMutation.data}
          originLabel={origin?.label ?? ""}
          destLabel={destination?.label ?? ""}
          advice={adviceMutation.data}
          adviceLoading={adviceMutation.isPending}
          onAnalyze={() => fireAdvice(planMutation.data!)}
          onAddStop={(stop) => {
            const labeled = relabelIfHome(stop, home);
            setExtraStops((prev) =>
              prev.some(
                (s) =>
                  Math.abs(s.lat - labeled.lat) < 0.0005 &&
                  Math.abs(s.lon - labeled.lon) < 0.0005,
              )
                ? prev
                : [...prev, labeled],
            );
          }}
          departureAt={departureAt}
        />
      )}
    </div>
  );
}

// DeparturePicker splits the departure datetime into native date + time
// pickers and exposes "Now" / "+1h" / "Tomorrow 8am" presets. Stores the
// combined value as a datetime-local string ("" = depart now).
function DeparturePicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const [date, time] = value.split("T");
  const setDate = (d: string) => onChange(d ? `${d}T${time || "08:00"}` : "");
  const setTime = (t: string) => onChange(date ? `${date}T${t}` : "");
  const toLocalDT = (d: Date) => {
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  };
  const presets: { label: string; build: () => string }[] = [
    {
      label: "+1h",
      build: () => toLocalDT(new Date(Date.now() + 60 * 60 * 1000)),
    },
    {
      label: "Tomorrow 8am",
      build: () => {
        const d = new Date();
        d.setDate(d.getDate() + 1);
        d.setHours(8, 0, 0, 0);
        return toLocalDT(d);
      },
    },
  ];
  return (
    <div className="flex flex-col gap-1 text-sm">
      <span className="text-neutral-400">Departure</span>
      <div className="flex flex-wrap items-center gap-2">
        <DatePopover value={date || ""} onChange={setDate} />
        <TimePopover value={time || ""} onChange={setTime} />
        {presets.map((p) => (
          <button
            key={p.label}
            type="button"
            onClick={() => onChange(p.build())}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-xs text-neutral-300 hover:border-neutral-500 hover:text-neutral-100"
          >
            {p.label}
          </button>
        ))}
        {value && (
          <button
            type="button"
            onClick={() => onChange("")}
            className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-xs text-neutral-500 hover:text-neutral-300"
          >
            Clear
          </button>
        )}
      </div>
      <span className="text-xs text-neutral-600">
        {value ? "Departure shifted; route times update accordingly" : "Depart now"}
      </span>
    </div>
  );
}

// useOutsideClick closes the popup when the user clicks anywhere
// outside the referenced element. Shared by DatePopover and TimePopover.
function useOutsideClick(
  ref: React.RefObject<HTMLElement | null>,
  onOutside: () => void,
  active: boolean,
) {
  useEffect(() => {
    if (!active) return;
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onOutside();
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [active, ref, onOutside]);
}

const MONTH_NAMES = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];
const DOW_LABELS = ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"];

// DatePopover renders a button showing the formatted date; clicking it
// opens a calendar grid styled to match the rest of the dark UI.
function DatePopover({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  useOutsideClick(containerRef, () => setOpen(false), open);

  const today = new Date();
  const selected = value ? parseLocalDate(value) : null;
  const initial = selected ?? today;
  const [viewYear, setViewYear] = useState(initial.getFullYear());
  const [viewMonth, setViewMonth] = useState(initial.getMonth());

  // Build a 6-week grid starting on Sunday. Days outside the view
  // month render dimmed so the grid stays a fixed 6×7 — no jitter
  // as the user pages between months.
  const firstOfMonth = new Date(viewYear, viewMonth, 1);
  const gridStart = new Date(firstOfMonth);
  gridStart.setDate(1 - firstOfMonth.getDay());
  const cells: Date[] = [];
  for (let i = 0; i < 42; i++) {
    const d = new Date(gridStart);
    d.setDate(gridStart.getDate() + i);
    cells.push(d);
  }

  const isSameDay = (a: Date, b: Date) =>
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate();

  const label = selected
    ? selected.toLocaleDateString([], { year: "numeric", month: "short", day: "numeric" })
    : "Select date";

  return (
    <div className="relative" ref={containerRef}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 hover:border-neutral-500 focus:outline-none"
      >
        <span className="text-neutral-500">📅</span>
        <span className={selected ? "" : "text-neutral-500"}>{label}</span>
      </button>
      {open && (
        <div className="absolute z-20 mt-1 w-72 rounded-md border border-neutral-700 bg-neutral-900 p-3 shadow-lg">
          <div className="mb-2 flex items-center justify-between">
            <button
              type="button"
              onClick={() => {
                const m = viewMonth - 1;
                if (m < 0) { setViewYear(viewYear - 1); setViewMonth(11); }
                else setViewMonth(m);
              }}
              className="rounded px-2 py-1 text-neutral-400 hover:bg-neutral-800 hover:text-neutral-100"
            >‹</button>
            <span className="text-sm text-neutral-200">
              {MONTH_NAMES[viewMonth]} {viewYear}
            </span>
            <button
              type="button"
              onClick={() => {
                const m = viewMonth + 1;
                if (m > 11) { setViewYear(viewYear + 1); setViewMonth(0); }
                else setViewMonth(m);
              }}
              className="rounded px-2 py-1 text-neutral-400 hover:bg-neutral-800 hover:text-neutral-100"
            >›</button>
          </div>
          <div className="grid grid-cols-7 gap-0.5 text-center text-xs">
            {DOW_LABELS.map((d) => (
              <div key={d} className="py-1 text-neutral-500">{d}</div>
            ))}
            {cells.map((d, i) => {
              const inMonth = d.getMonth() === viewMonth;
              const isToday = isSameDay(d, today);
              const isSelected = selected && isSameDay(d, selected);
              return (
                <button
                  key={i}
                  type="button"
                  onClick={() => {
                    onChange(formatLocalDate(d));
                    setOpen(false);
                  }}
                  className={`rounded py-1.5 text-sm transition-colors ${
                    isSelected
                      ? "bg-emerald-700 text-emerald-50"
                      : inMonth
                        ? "text-neutral-200 hover:bg-neutral-800"
                        : "text-neutral-600 hover:bg-neutral-800"
                  } ${isToday && !isSelected ? "ring-1 ring-neutral-600" : ""}`}
                >
                  {d.getDate()}
                </button>
              );
            })}
          </div>
          <div className="mt-2 flex justify-between border-t border-neutral-800 pt-2">
            <button
              type="button"
              onClick={() => { onChange(formatLocalDate(new Date())); setOpen(false); }}
              className="text-xs text-neutral-400 hover:text-neutral-100"
            >Today</button>
            {value && (
              <button
                type="button"
                onClick={() => { onChange(""); setOpen(false); }}
                className="text-xs text-neutral-500 hover:text-neutral-300"
              >Clear</button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// TimePopover renders a button with the formatted time; clicking it
// opens hour/minute scrollers in a popup styled like the rest of the UI.
function TimePopover({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  useOutsideClick(containerRef, () => setOpen(false), open);

  const [hh, mm] = value ? value.split(":") : ["", ""];
  const pad = (n: number) => String(n).padStart(2, "0");
  const label = value
    ? new Date(`2000-01-01T${value}`).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : "Time";

  return (
    <div className="relative" ref={containerRef}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-2 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 hover:border-neutral-500 focus:outline-none"
      >
        <span className="text-neutral-500">🕐</span>
        <span className={value ? "" : "text-neutral-500"}>{label}</span>
      </button>
      {open && (
        <div className="absolute z-20 mt-1 flex gap-2 rounded-md border border-neutral-700 bg-neutral-900 p-2 shadow-lg">
          <ScrollColumn
            options={Array.from({ length: 24 }, (_, i) => pad(i))}
            selected={hh}
            onSelect={(h) => onChange(`${h}:${mm || "00"}`)}
            label="Hour"
          />
          <ScrollColumn
            options={Array.from({ length: 12 }, (_, i) => pad(i * 5))}
            selected={mm}
            onSelect={(m) => onChange(`${hh || pad(new Date().getHours())}:${m}`)}
            label="Min"
          />
        </div>
      )}
    </div>
  );
}

function ScrollColumn({
  options,
  selected,
  onSelect,
  label,
}: {
  options: string[];
  selected: string;
  onSelect: (v: string) => void;
  label: string;
}) {
  return (
    <div className="flex flex-col items-center">
      <div className="mb-1 text-xs text-neutral-500">{label}</div>
      <div className="h-40 w-12 overflow-y-auto rounded border border-neutral-800">
        {options.map((opt) => (
          <button
            key={opt}
            type="button"
            onClick={() => onSelect(opt)}
            className={`block w-full px-2 py-1 text-sm tabular-nums transition-colors ${
              opt === selected
                ? "bg-emerald-700 text-emerald-50"
                : "text-neutral-300 hover:bg-neutral-800"
            }`}
          >
            {opt}
          </button>
        ))}
      </div>
    </div>
  );
}

// formatLocalDate / parseLocalDate avoid the UTC interpretation that
// `new Date("YYYY-MM-DD")` triggers (which can shift the day on the
// user's tz). Always work in local time for the date portion.
function formatLocalDate(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}
function parseLocalDate(s: string): Date {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(y, m - 1, d);
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
    const t = setTimeout(() => setDebounced(query), 150);
    return () => clearTimeout(t);
  }, [query]);

  const results = useQuery({
    queryKey: ["geocode", debounced],
    queryFn: () => backend.geocode(debounced, 8),
    enabled: debounced.trim().length >= 2,
    staleTime: 5 * 60 * 1000,
  });

  // Pending: either debounce hasn't fired yet or query is in flight.
  const pending = query !== debounced || results.isFetching;

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
  const showDropdown = open && query.trim().length >= 2;

  return (
    <div className="relative" ref={containerRef}>
      <div className="relative">
        <input
          type="text"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setOpen(true);
          }}
          onFocus={() => setOpen(true)}
          placeholder={placeholder}
          className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 pr-8 text-neutral-100 focus:border-neutral-500 focus:outline-none"
        />
        {pending && query.trim().length >= 2 && (
          <span className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2">
            <Spinner />
          </span>
        )}
      </div>
      {showDropdown && (items.length > 0 ? (
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
      ) : !pending && results.isFetched ? (
        <div className="absolute left-0 right-0 top-[calc(100%+2px)] z-10 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-xs text-neutral-500 shadow-lg">
          No results
        </div>
      ) : null)}
    </div>
  );
}

function formatGeocode(r: GeocodeResult): string {
  return [r.name, r.admin1, r.country].filter(Boolean).join(", ");
}

type StopAdder = (stop: { lat: number; lon: number; label: string }) => void;

// ViaStopList renders existing via stops with remove buttons and a
// search field for adding new ones by geocode.
function ViaStopList({
  stops,
  onRemove,
  onAdd,
}: {
  stops: NonNullable<Selection>[];
  onRemove: (i: number) => void;
  onAdd: StopAdder;
}) {
  const [adding, setAdding] = useState(false);
  return (
    <div className="space-y-2">
      {stops.map((stop, i) => (
        <div
          key={i}
          className="flex items-center gap-2 rounded-lg border border-neutral-800 bg-neutral-950/40 px-3 py-2 text-sm"
        >
          <span className="shrink-0 text-neutral-500">Via:</span>
          <span className="flex-1 text-neutral-100">{stop.label}</span>
          <button
            type="button"
            onClick={() => onRemove(i)}
            className="shrink-0 text-xs text-neutral-500 hover:text-neutral-300"
          >
            remove
          </button>
        </div>
      ))}
      {adding ? (
        <div className="rounded-lg border border-neutral-800 bg-neutral-950/40 px-3 py-2">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs text-neutral-400">Add a stop</span>
            <button
              type="button"
              onClick={() => setAdding(false)}
              className="text-xs text-neutral-500 hover:text-neutral-300"
            >
              cancel
            </button>
          </div>
          <LocationSearch
            placeholder="Search city or place…"
            onSelect={(r) => {
              onAdd({ lat: r.latitude, lon: r.longitude, label: formatGeocode(r) });
              setAdding(false);
            }}
          />
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setAdding(true)}
          className="text-xs text-neutral-500 hover:text-neutral-300"
        >
          + add via stop
        </button>
      )}
    </div>
  );
}

function TripPlanResult({
  plan,
  originLabel,
  destLabel,
  advice,
  adviceLoading,
  onAnalyze,
  onAddStop,
  departureAt,
}: {
  plan: TripPlan;
  originLabel: string;
  destLabel: string;
  advice?: TripAdvice;
  adviceLoading?: boolean;
  onAnalyze?: () => void;
  onAddStop?: StopAdder;
  departureAt?: string;
}) {
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
      <TripAdviceCard advice={advice} loading={adviceLoading ?? false} onAnalyze={onAnalyze} />
      {plan.Routes.map((route, i) => (
        <RouteCard key={i} route={route} index={i} originLabel={originLabel} destLabel={destLabel} onAddStop={onAddStop} departureAt={departureAt} />
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

// formatWaypointTime formats a UTC ISO string to local HH:MM, applying
// an optional millisecond shift (user departure offset vs Rivian's "now").
function formatWaypointTime(utcStr: string | undefined, shiftMs: number): string {
  if (!utcStr) return "";
  const d = new Date(new Date(utcStr).getTime() + shiftMs);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// formatDuration turns a seconds count into "Hh Mm" / "Mm" suitable
// for the route summary header.
function formatDuration(seconds: number): string {
  const m = Math.round(seconds / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  const rem = m % 60;
  if (rem === 0) return `${h}h`;
  return `${h}h ${rem}m`;
}

function RouteCard({
  route,
  index,
  originLabel,
  destLabel,
  onAddStop,
  departureAt,
}: {
  route: TripRoute;
  index: number;
  originLabel: string;
  destLabel: string;
  onAddStop?: StopAdder;
  departureAt?: string;
}) {
  // Rivian's planTrip2 returns waypointType in lowercase ("origin" /
  // "destination" / "waypoint"); compare case-insensitively so the
  // table renders correctly regardless of casing.
  const wpType = (w: { WaypointType: string }) => w.WaypointType.toLowerCase();
  const charging = route.Waypoints.filter(
    (w) => wpType(w) !== "origin" && wpType(w) !== "destination" && wpType(w) !== "waypoint" && wpType(w) !== "other",
  );
  const origin = route.Waypoints.find((w) => wpType(w) === "origin");
  const dest = route.Waypoints.find((w) => wpType(w) === "destination");
  const totalChargeMin = Math.round(route.TotalChargingDurationSec / 60);
  const showTable = origin || dest || charging.length > 0;

  // Shift displayed times so that the origin departure matches the
  // user's chosen departure datetime. Rivian plans from "now", so
  // the delta is (userDeparture - originDepartureTimeUTC).
  const timeShiftMs = (() => {
    if (!departureAt) return 0;
    const userDep = new Date(departureAt).getTime();
    const rivianDep = origin?.DepartureTimeUTC
      ? new Date(origin.DepartureTimeUTC).getTime()
      : Date.now();
    return userDep - rivianDep;
  })();
  return (
    <Card title={`Route ${index + 1}${route.DestinationReached ? "" : " — destination unreachable"}`}>
      <div className="mb-4">
        <Suspense fallback={<div className="h-80 animate-pulse rounded-lg border border-neutral-800 bg-neutral-900/50" />}>
          <TripRouteMap route={route} onAddStop={onAddStop} />
        </Suspense>
      </div>
      <dl className="grid grid-cols-2 gap-y-2 gap-x-6 text-sm sm:grid-cols-4">
        <Stat
          label="Distance"
          value={route.TotalDriveDistanceMeters > 0
            ? `${(route.TotalDriveDistanceMeters / 1609.344).toFixed(0)} mi`
            : "—"}
        />
        <Stat
          label="Total time"
          value={route.TotalTripDurationSec > 0
            ? formatDuration(route.TotalTripDurationSec)
            : "—"}
        />
        <Stat
          label="Arrival"
          value={
            (() => {
              // Prefer Rivian's per-waypoint arrival time when present.
              // Fall back to (departure + totalTripDurationSec) so the
              // stat always reads something useful — Rivian sometimes
              // omits arrivalTimeUTC on the destination waypoint.
              let t = formatWaypointTime(dest?.ArrivalTimeUTC, timeShiftMs);
              if (!t && route.TotalTripDurationSec > 0) {
                const originDep = origin?.DepartureTimeUTC
                  ? new Date(origin.DepartureTimeUTC).getTime()
                  : Date.now();
                const arriveMs = originDep + route.TotalTripDurationSec * 1000 + timeShiftMs;
                t = new Date(arriveMs).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
              }
              const soc = route.ArrivalSoC > 0 ? `${route.ArrivalSoC.toFixed(0)}%` : "—";
              return t ? `${t} · ${soc}` : soc;
            })()
          }
        />
        <Stat
          label="Charging"
          value={
            charging.length > 0
              ? `${charging.length} stop${charging.length === 1 ? "" : "s"} · ${totalChargeMin} min`
              : "0 stops"
          }
        />
      </dl>
      {showTable && (
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
              {origin && (
                <tr className="border-b border-neutral-900 text-neutral-400">
                  <td className="px-2 py-2">S</td>
                  <td className="px-2 py-2">{originLabel || origin.Name || "Origin"}</td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2 font-mono text-neutral-100">
                    {formatWaypointTime(origin.DepartureTimeUTC, timeShiftMs) && (
                      <div className="text-xs text-neutral-500">{formatWaypointTime(origin.DepartureTimeUTC, timeShiftMs)}</div>
                    )}
                    {origin.DepartureSoC > 0 ? `${origin.DepartureSoC.toFixed(0)}%` : "—"}
                  </td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2"></td>
                </tr>
              )}
              {charging.map((w, j) => (
                <tr key={j} className="border-b border-neutral-900">
                  <td className="px-2 py-2 text-neutral-500">{j + 1}</td>
                  <td className="px-2 py-2">{w.Name || `(${w.Latitude.toFixed(3)}, ${w.Longitude.toFixed(3)})`}</td>
                  <td className="px-2 py-2 font-mono">
                    {formatWaypointTime(w.ArrivalTimeUTC, timeShiftMs) && (
                      <div className="text-xs text-neutral-500">{formatWaypointTime(w.ArrivalTimeUTC, timeShiftMs)}</div>
                    )}
                    {w.ArrivalSoC.toFixed(0)}%
                  </td>
                  <td className="px-2 py-2 font-mono">
                    {formatWaypointTime(w.DepartureTimeUTC, timeShiftMs) && (
                      <div className="text-xs text-neutral-500">{formatWaypointTime(w.DepartureTimeUTC, timeShiftMs)}</div>
                    )}
                    {w.DepartureSoC.toFixed(0)}%
                  </td>
                  <td className="px-2 py-2 font-mono">{Math.round(w.ChargeDurationSec / 60)} min</td>
                  <td className="px-2 py-2 font-mono">{w.MaxPowerKW > 0 ? w.MaxPowerKW.toFixed(0) : "—"}</td>
                  <td className="px-2 py-2">{w.AdapterRequired ? "yes" : ""}</td>
                </tr>
              ))}
              {dest && (
                <tr className="border-b border-neutral-900 text-neutral-400">
                  <td className="px-2 py-2">E</td>
                  <td className="px-2 py-2">{destLabel || dest.Name || "Destination"}</td>
                  <td className="px-2 py-2 font-mono text-neutral-100">
                    {formatWaypointTime(dest.ArrivalTimeUTC, timeShiftMs) && (
                      <div className="text-xs text-neutral-500">{formatWaypointTime(dest.ArrivalTimeUTC, timeShiftMs)}</div>
                    )}
                    {dest.ArrivalSoC > 0 ? `${dest.ArrivalSoC.toFixed(0)}%` : "—"}
                  </td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2"></td>
                </tr>
              )}
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

function TripAdviceCard({
  advice,
  loading,
  onAnalyze,
}: {
  advice?: TripAdvice;
  loading: boolean;
  onAnalyze?: () => void;
}) {
  return (
    <Card title="Trip analysis">
      {!loading && !advice && (
        <button
          type="button"
          onClick={onAnalyze}
          disabled={!onAnalyze}
          className="rounded-md bg-neutral-800 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-700 disabled:cursor-not-allowed disabled:opacity-40"
        >
          Analyze plan
        </button>
      )}
      {loading && !advice && (
        <div className="flex items-center gap-2 text-sm text-neutral-400">
          <Spinner />
          <span>Analyzing plan…</span>
        </div>
      )}
      {advice && (
        <div className="space-y-4">
          {advice.headline && (
            <p className="font-medium text-neutral-100">{advice.headline}</p>
          )}
          <AdviceCostStrip cost={advice.cost_estimate} />
          <AdviceSection label="Cost" items={advice.cost} accent="emerald" />
          <AdviceSection label="Efficiency" items={advice.efficiency} accent="sky" />
          <AdviceSection label="Weather" items={advice.weather} accent="cyan" />
          <AdviceSection label="Vehicle" items={advice.vehicle} accent="amber" />
          {advice.model && (
            <p className="text-xs text-neutral-600">{advice.model}</p>
          )}
        </div>
      )}
    </Card>
  );
}

// AdviceCostStrip renders the deterministic cost numbers the server
// computed (not the LLM). Always shown when there's any signal —
// purely-DCFC trips show only the DCFC line, home-energy trips show
// only the home equivalent.
function AdviceCostStrip({ cost }: { cost: TripAdvice["cost_estimate"] }) {
  const hasDCFC = cost.dcfc_spend > 0;
  const hasHome = cost.home_equivalent > 0;
  if (!hasDCFC && !hasHome) return null;
  const fmt = (v: number) =>
    new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: cost.currency || "USD",
    }).format(v);
  return (
    <div className="flex flex-wrap gap-4 rounded-md border border-neutral-800 bg-neutral-900/60 px-3 py-2 text-sm">
      {hasDCFC && (
        <div>
          <div className="text-xs uppercase tracking-wide text-neutral-500">DCFC spend</div>
          <div className="font-mono text-neutral-100">{fmt(cost.dcfc_spend)}</div>
          <div className="text-[10px] text-neutral-600">@ {fmt(cost.dcfc_rate_used)}/kWh</div>
        </div>
      )}
      {hasHome && (
        <div>
          <div className="text-xs uppercase tracking-wide text-neutral-500">Home-rate equivalent</div>
          <div className="font-mono text-neutral-100">{fmt(cost.home_equivalent)}</div>
          <div className="text-[10px] text-neutral-600">@ {fmt(cost.home_rate_used)}/kWh</div>
        </div>
      )}
    </div>
  );
}

// AdviceSection renders one category of LLM-written observations.
// Hidden entirely when the model returned nothing for that category
// so we don't show empty headers.
function AdviceSection({
  label,
  items,
  accent,
}: {
  label: string;
  items: string[] | undefined;
  accent: "emerald" | "sky" | "cyan" | "amber";
}) {
  if (!items || items.length === 0) return null;
  const dot = {
    emerald: "text-emerald-500",
    sky: "text-sky-500",
    cyan: "text-cyan-500",
    amber: "text-amber-500",
  }[accent];
  return (
    <div>
      <div className="mb-1 text-xs uppercase tracking-wide text-neutral-500">{label}</div>
      <ul className="space-y-1.5 text-sm text-neutral-300">
        {items.map((ins, i) => (
          <li key={i} className="flex gap-2">
            <span className={`mt-0.5 shrink-0 ${dot}`}>·</span>
            <span>{ins}</span>
          </li>
        ))}
      </ul>
    </div>
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
