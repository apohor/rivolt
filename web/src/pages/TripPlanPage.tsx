import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, vehicleNeedsTeslaAdapter, type GeocodeResult, type PlannerFavorite, type SavedTrip, type SavedTripInputs, type TripAdvice, type TripCostEstimate, type TripPlan, type TripPlanMultidayResponse, type TripRoute } from "../lib/api";
import { Card, ErrorBoundary, ErrorBox, PageHeader, Spinner } from "../components/ui";
import ConnectRivianPrompt from "../components/ConnectRivianPrompt";
import { useAIEnabled } from "../lib/config";

// Lazy-load TripRouteMap so the Leaflet + Protomaps + protomaps-leaflet
// bundle (several hundred KB) only ships when the user actually opens
// a plan with a rendered map, not on every page navigation.
const TripRouteMap = lazy(() =>
  import("../components/TripRouteMap").then((m) => ({ default: m.TripRouteMap })),
);

// Preset destinations come from the user's saved favorites
// (settings.planner.favorites) now; the planner used to ship a
// hardcoded Texas list but that's wrong as soon as a user lives
// anywhere else.

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

// collectInputs folds the current form state into the SavedTripInputs
// shape the backend persists. Centralized so save + future "what
// changed since the snapshot?" comparisons stay in sync.
function collectInputs(s: {
  origin: NonNullable<Selection>;
  destination: NonNullable<Selection>;
  extraStops: NonNullable<Selection>[];
  targetSoc: string;
  startingSoc: string;
  driveMode: DriveMode;
  hasAdapter: boolean;
  departureAt: string;
}): SavedTripInputs {
  return {
    origin: s.origin,
    destination: s.destination,
    extra_stops: s.extraStops,
    target_soc: s.targetSoc,
    starting_soc: s.startingSoc,
    drive_mode: s.driveMode,
    has_adapter: s.hasAdapter,
    departure_at: s.departureAt,
  };
}

// DriveMode is restricted to the two values the dropdown surfaces.
// Older saved-trip / planner-prefs payloads may still carry empty
// or now-retired values (SPORT/WINTER/OFF_ROAD_AUTO); the
// normalizeDriveMode helper below coerces them to EVERYDAY so the
// controlled select always matches an option and Rivian gets a
// valid enum.
type DriveMode = "EVERYDAY" | "DISTANCE";

function normalizeDriveMode(v: unknown): DriveMode {
  return v === "DISTANCE" ? "DISTANCE" : "EVERYDAY";
}

export default function TripPlanPage() {
  const [origin, setOrigin] = useState<Selection>(null);
  const [destination, setDestination] = useState<Selection>(null);
  const [extraStops, setExtraStops] = useState<NonNullable<Selection>[]>([]);
  // Parallel array to extraStops: true marks that stop as an overnight
  // (ends one day, starts the next). Any true value flips planning to
  // the multi-day endpoint. Defaults baked: 10h parked, 7kW L2.
  const [overnightFlags, setOvernightFlags] = useState<boolean[]>([]);
  // Overnight charging limit. Three modes:
  //   - "soc":     cap the post-charge SoC at maxOvernightSoCPct
  //   - "time":    cap by hours plugged in (overnightParkedHours)
  //   - "depart":  cap by a target morning departure clock time
  //                (overnightDepartureLocal, HH:MM). Translated to
  //                parked hours assuming a typical 20:00 arrival;
  //                Time-mode style otherwise (SoC cap lifted to 100).
  const [overnightLimitMode, setOvernightLimitMode] = useState<"soc" | "time" | "depart">("soc");
  const [maxOvernightSoCPct, setMaxOvernightSoCPct] = useState<number>(80);
  const [overnightParkedHours, setOvernightParkedHours] = useState<number>(8);
  const [overnightDepartureLocal, setOvernightDepartureLocal] = useState<string>("08:00");
  const [targetSoc, setTargetSoc] = useState<string>("20");
  // Empty = auto from live vehicle state; user can override.
  const [startingSoc, setStartingSoc] = useState<string>("");
  const [driveMode, setDriveMode] = useState<DriveMode>("EVERYDAY");
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

  // Pulled up earlier than the savedTrips block below so the
  // favorites mutation can reference it for cache updates.
  const qcEarly = useQueryClient();
  const homeQuery = useQuery({
    queryKey: ["settings", "homeLocation"],
    queryFn: backend.homeLocationGet,
    staleTime: 60 * 1000,
  });
  const home = homeQuery.data?.set ? homeQuery.data : null;
  const favoritesQuery = useQuery({
    queryKey: ["settings", "plannerFavorites"],
    queryFn: backend.plannerFavoritesGet,
    staleTime: 60 * 1000,
  });
  const favorites = favoritesQuery.data ?? [];
  const favoritesMutation = useMutation({
    mutationFn: (list: PlannerFavorite[]) => backend.plannerFavoritesPut(list),
    onSuccess: (data) => {
      qcEarly.setQueryData(["settings", "plannerFavorites"], data);
    },
  });
  // Add the current selection to favorites with a user-supplied
  // name. We prompt rather than auto-name because the geocoded
  // label is often a full address — see the same compactness
  // tradeoff that drove the Home rename. Idempotent: if a
  // coordinate within 100m is already saved, skip the add.
  const saveAsFavorite = (sel: { lat: number; lon: number; label: string }) => {
    const dupe = favorites.find(
      (f) =>
        haversineMeters(
          { lat: f.latitude, lon: f.longitude },
          { lat: sel.lat, lon: sel.lon },
        ) < 100,
    );
    if (dupe) return;
    const defaultName = sel.label.length > 30 ? "" : sel.label;
    const name = window.prompt("Save as favorite — pick a short name:", defaultName);
    if (!name || !name.trim()) return;
    const next: PlannerFavorite[] = [
      ...favorites,
      {
        id: crypto.randomUUID(),
        label: name.trim(),
        latitude: sel.lat,
        longitude: sel.lon,
      },
    ];
    favoritesMutation.mutate(next);
  };

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
    {
      const restored = (last.extraStops ?? []).map((s) => relabelIfHome(s, home));
      setExtraStops(restored);
      setOvernightFlags(restored.map(() => false));
    }
    if (last.targetSoc) setTargetSoc(last.targetSoc);
    if (last.driveMode) setDriveMode(normalizeDriveMode(last.driveMode));
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
      setDriveMode(normalizeDriveMode(prefsQuery.data.drive_mode));
    }
    if (typeof prefsQuery.data.has_adapter === "boolean") {
      setHasAdapter(prefsQuery.data.has_adapter);
    }
    setPrefsApplied(true);
  }, [prefsQuery.data, prefsApplied]);

  // autoReplanAfterAddRef: ref-flag set by the map's onAddStop /
  // preview-confirm path so the next render auto-fires the planner.
  // Ref (not state) because we only care about latch-and-fire on the
  // immediately-following render, not as a render input itself.
  const autoReplanAfterAddRef = useRef(false);
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
        pack_kwh:
          typeof firstVehicle?.pack_kwh === "number" && firstVehicle.pack_kwh > 0
            ? firstVehicle.pack_kwh
            : undefined,
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
      setLoadedSnapshot(null);
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

  // Multi-day mutation. Fires instead of planMutation when any
  // via-stop is marked Overnight. Server orchestrates N+1 planTrip2
  // calls and returns one Day per leg.
  const hasOvernightStops = overnightFlags.some(Boolean);
  const multidayMutation = useMutation({
    mutationFn: () => {
      if (!origin || !destination) {
        return Promise.reject(new Error("origin + destination required"));
      }
      const vid = firstVehicle?.rivian_vehicle_id;
      const liveSoc = stateQuery.data?.battery_level_pct;
      const manualSoc = startingSoc.trim() !== "" ? Number(startingSoc) : undefined;
      const soc = manualSoc ?? (typeof liveSoc === "number" && liveSoc > 0 ? liveSoc : undefined);
      const target = targetSoc.trim() === "" ? undefined : Number(targetSoc);
      if (!vid || !soc || !firstVehicle?.pack_kwh) {
        return Promise.reject(new Error("vehicle, starting SoC, and pack capacity required"));
      }
      // Resolve mode → per-overnight parked_hours.
      // - "soc":    leave 10 (server default), let the SoC cap rule.
      // - "time":   take the user's number directly.
      // - "depart": translate "I want to depart at HH:MM" into hours
      //             assuming a 20:00 typical arrival. ASSUMED_ARRIVAL_HR
      //             is conservative for road trips — late dinner, hotel
      //             check-in. Real arrival varies per leg, but the
      //             planner is an estimator, not a clock.
      const ASSUMED_ARRIVAL_HR = 20;
      const hoursForOvernight = (() => {
        if (overnightLimitMode === "time") return overnightParkedHours;
        if (overnightLimitMode === "depart") {
          const [hh, mm] = overnightDepartureLocal.split(":").map(Number);
          const depFrac = (hh || 0) + (mm || 0) / 60;
          const parked = (24 - ASSUMED_ARRIVAL_HR) + depFrac;
          return Math.max(1, Math.min(24, Math.round(parked)));
        }
        return 10;
      })();
      const overnights = extraStops
        .map((s, i) => ({ stop: s, on: !!overnightFlags[i] }))
        .filter((x) => x.on)
        .map((x) => ({
          latitude: x.stop.lat,
          longitude: x.stop.lon,
          name: x.stop.label,
          parked_hours: hoursForOvernight,
          l2_kw: 7,
        }));
      return backend.planTripMultiday({
        vehicle_id: vid,
        starting_soc: soc,
        pack_kwh: firstVehicle.pack_kwh,
        drive_mode: driveMode || undefined,
        has_adapter: hasAdapter,
        target_arrival_soc_percent: target,
        origin: { latitude: origin.lat, longitude: origin.lon, waypoint_type: "origin" },
        destination: { latitude: destination.lat, longitude: destination.lon, waypoint_type: "destination" },
        overnights,
        max_overnight_soc_pct: overnightLimitMode === "soc" ? maxOvernightSoCPct : 100,
      });
    },
  });

  // Auto-replan trigger: when extraStops grows from the map (preview
  // confirm or legacy add-as-waypoint), planMutation re-fires once
  // React has flushed the new extraStops into the mutationFn closure.
  // Latch-and-clear via ref so the effect doesn't fire on unrelated
  // extraStops changes (saved-trip load, ViaStopList edits, etc.).
  useEffect(() => {
    if (!autoReplanAfterAddRef.current) return;
    autoReplanAfterAddRef.current = false;
    planMutation.mutate();
    // planMutation.mutate is stable; intentionally excluding it to
    // avoid re-firing when the mutation completes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [extraStops]);

  // Saved trips. loadedSnapshot, when non-null, is what the result
  // card renders instead of the live planMutation.data — that's how
  // clicking a saved trip can show the historical map + waypoint
  // table instantly without a fresh Rivian round-trip. Replan clears
  // it back to the live path.
  const qc = useQueryClient();
  const savedTripsQuery = useQuery({
    queryKey: ["savedTrips"],
    queryFn: backend.savedTripsList,
    staleTime: 30 * 1000,
  });
  const [loadedSnapshot, setLoadedSnapshot] = useState<
    { plan: TripPlan; advice?: TripAdvice; name: string; updatedAt: string } | null
  >(null);
  const [activeTripId, setActiveTripId] = useState<string | null>(null);
  const saveTripMutation = useMutation({
    mutationFn: async (vars: { name: string; id: string | null }) => {
      if (!origin || !destination) {
        throw new Error("plan a trip before saving");
      }
      const plan = loadedSnapshot?.plan ?? planMutation.data ?? undefined;
      const advice = loadedSnapshot?.advice ?? adviceMutation.data ?? undefined;
      const body = {
        name: vars.name,
        inputs: collectInputs({
          origin,
          destination,
          extraStops,
          targetSoc,
          startingSoc,
          driveMode,
          hasAdapter,
          departureAt,
        }),
        plan,
        advice,
      };
      return vars.id
        ? backend.savedTripUpdate(vars.id, body)
        : backend.savedTripCreate(body);
    },
    onSuccess: (t) => {
      qc.invalidateQueries({ queryKey: ["savedTrips"] });
      setActiveTripId(t.id);
    },
  });
  const deleteTripMutation = useMutation({
    mutationFn: (id: string) => backend.savedTripDelete(id),
    onSuccess: (_, id) => {
      qc.invalidateQueries({ queryKey: ["savedTrips"] });
      if (activeTripId === id) {
        setActiveTripId(null);
        setLoadedSnapshot(null);
      }
    },
  });

  const loadSavedTrip = (t: SavedTrip) => {
    const i = t.inputs;
    setOrigin(i.origin);
    setDestination(i.destination);
    setExtraStops(i.extra_stops ?? []);
    setOvernightFlags((i.extra_stops ?? []).map(() => false));
    if (typeof i.target_soc === "string") setTargetSoc(i.target_soc);
    if (typeof i.starting_soc === "string") setStartingSoc(i.starting_soc);
    if (typeof i.drive_mode === "string") setDriveMode(normalizeDriveMode(i.drive_mode));
    if (typeof i.has_adapter === "boolean") setHasAdapter(i.has_adapter);
    if (typeof i.departure_at === "string") setDepartureAt(i.departure_at);
    setActiveTripId(t.id);
    planMutation.reset();
    adviceMutation.reset();
    if (t.plan) {
      setLoadedSnapshot({
        plan: t.plan,
        advice: t.advice,
        name: t.name,
        updatedAt: t.updated_at,
      });
    } else {
      setLoadedSnapshot(null);
    }
  };

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
    if (hasOvernightStops) {
      planMutation.reset();
      multidayMutation.mutate();
    } else {
      multidayMutation.reset();
      planMutation.mutate();
    }
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
                <option value="EVERYDAY">All-Purpose</option>
                <option value="DISTANCE">Conserve</option>
              </select>
            </label>
            {vehicleNeedsTeslaAdapter(profileQuery.data, firstVehicle?.model_year) && (
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
            )}
          </div>
          <LocationField
            heading="From"
            value={origin}
            onChange={(s) => setOrigin(s ? relabelIfHome(s, home) : null)}
            onSaveAsFavorite={saveAsFavorite}
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
              ...favorites.map((f) => ({
                label: f.label,
                lat: f.latitude,
                lon: f.longitude,
              })),
            ]}
          />
          <ViaStopList
            stops={extraStops}
            overnightFlags={overnightFlags}
            onRemove={(i) => {
              setExtraStops((prev) => prev.filter((_, j) => j !== i));
              setOvernightFlags((prev) => prev.filter((_, j) => j !== i));
            }}
            onToggleOvernight={(i, on) =>
              setOvernightFlags((prev) => {
                const next = [...prev];
                while (next.length < extraStops.length) next.push(false);
                next[i] = on;
                return next;
              })
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
              setOvernightFlags((prev) => [...prev, false]);
            }}
          />
          {hasOvernightStops && (
            <div
              className="flex flex-wrap items-center gap-2 text-xs text-neutral-300"
              title="How to limit overnight L2 charging: cap the post-charge SoC, cap by hours plugged in, or cap by next-day departure time."
            >
              <span className="text-neutral-400">Limit overnight by</span>
              <div className="inline-flex overflow-hidden rounded-md border border-neutral-700 text-[11px]">
                {(["soc", "time", "depart"] as const).map((m) => {
                  const active = overnightLimitMode === m;
                  const label = m === "soc" ? "SoC" : m === "time" ? "Time" : "Depart";
                  return (
                    <button
                      key={m}
                      type="button"
                      onClick={() => setOvernightLimitMode(m)}
                      className={`px-2 py-0.5 transition-colors ${
                        active
                          ? "bg-emerald-600/20 text-emerald-300"
                          : "text-neutral-400 hover:text-neutral-200"
                      }`}
                    >
                      {label}
                    </button>
                  );
                })}
              </div>
              {overnightLimitMode === "soc" && (
                <label className="flex items-center gap-1">
                  <input
                    type="number"
                    min={50}
                    max={100}
                    step={5}
                    value={maxOvernightSoCPct}
                    onChange={(e) => setMaxOvernightSoCPct(Number(e.target.value) || 80)}
                    className="w-16 rounded border border-neutral-700 bg-neutral-900 px-2 py-0.5 text-neutral-200 tabular-nums"
                  />
                  <span className="text-neutral-400">%</span>
                </label>
              )}
              {overnightLimitMode === "time" && (
                <label className="flex items-center gap-1">
                  <input
                    type="number"
                    min={1}
                    max={24}
                    step={1}
                    value={overnightParkedHours}
                    onChange={(e) => setOvernightParkedHours(Number(e.target.value) || 8)}
                    className="w-16 rounded border border-neutral-700 bg-neutral-900 px-2 py-0.5 text-neutral-200 tabular-nums"
                  />
                  <span className="text-neutral-400">h plugged in</span>
                </label>
              )}
              {overnightLimitMode === "depart" && (
                <label
                  className="flex items-center gap-1"
                  title="Translated to parked hours assuming a typical 20:00 arrival."
                >
                  <span className="text-neutral-400">at</span>
                  <input
                    type="time"
                    value={overnightDepartureLocal}
                    onChange={(e) => setOvernightDepartureLocal(e.target.value || "08:00")}
                    className="rounded border border-neutral-700 bg-neutral-900 px-2 py-0.5 text-neutral-200 tabular-nums"
                  />
                  <span className="text-neutral-400">next morning</span>
                </label>
              )}
            </div>
          )}
          <LocationField
            heading="To"
            value={destination}
            onChange={(s) => setDestination(s ? relabelIfHome(s, home) : null)}
            onSaveAsFavorite={saveAsFavorite}
            presets={[
              ...(home
                ? [{ label: home.label || "Home", lat: home.latitude, lon: home.longitude }]
                : []),
              ...favorites.map((f) => ({
                label: f.label,
                lat: f.latitude,
                lon: f.longitude,
              })),
            ]}
          />
          <div className="flex items-center gap-3">
            <button
              type="submit"
              disabled={planMutation.isPending || multidayMutation.isPending || !origin || !destination}
              className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-medium text-emerald-50 hover:bg-emerald-600 disabled:cursor-not-allowed disabled:bg-neutral-800 disabled:text-neutral-500"
            >
              {hasOvernightStops ? `Plan ${overnightFlags.filter(Boolean).length + 1}-day trip` : "Plan trip"}
            </button>
            {(planMutation.isPending || multidayMutation.isPending) && <Spinner />}
            <span className="text-xs text-neutral-500">
              Tip: configure your charging settings in{" "}
              <a
                href="/settings?tab=charging"
                className="underline hover:text-neutral-300"
              >
                Settings &rarr; Charging
              </a>{" "}
              to see accurate per-stop costs and membership savings.
            </span>
          </div>
        </form>
      </Card>

      <SavedTripsCard
        trips={savedTripsQuery.data ?? []}
        loading={savedTripsQuery.isLoading}
        activeId={activeTripId}
        canSave={!!origin && !!destination}
        saveDisabled={saveTripMutation.isPending}
        onLoad={loadSavedTrip}
        onDelete={(id) => deleteTripMutation.mutate(id)}
        onSave={(name, id) => saveTripMutation.mutate({ name, id })}
        saveError={(saveTripMutation.error as Error | null)?.message}
      />

      {planMutation.error && (
        <ErrorBox
          title="Planner failed"
          detail={(planMutation.error as Error).message}
        />
      )}

      {multidayMutation.isError && (
        <ErrorBox
          title="Multi-day plan failed"
          detail={(multidayMutation.error as Error).message}
        />
      )}

      {multidayMutation.data && (
        <MultidayResult
          response={multidayMutation.data}
          originLabel={origin?.label ?? ""}
          destLabel={destination?.label ?? ""}
          departureAt={departureAt}
        />
      )}

      {(() => {
        // Don't render the single-day card if the user just produced
        // a multi-day plan — the form's current state is multi-day.
        if (multidayMutation.data) return null;
        const displayPlan = loadedSnapshot?.plan ?? planMutation.data;
        const displayAdvice = loadedSnapshot?.advice ?? adviceMutation.data;
        if (!displayPlan) return null;
        // The plan + advice shapes have evolved over releases; a
        // snapshot saved against an older schema can throw during
        // render. resetKey on activeTripId clears the boundary when
        // the user switches trips, and onReset drops the loaded
        // snapshot back to the live mutation path.
        return (
          <ErrorBoundary
            resetKey={activeTripId ?? "live"}
            onReset={() => setLoadedSnapshot(null)}
            fallback={(err, reset) => (
              <div
                role="alert"
                className="rounded-lg border border-rose-900 bg-rose-950/40 px-4 py-3 text-sm text-rose-200"
              >
                <div className="font-semibold">
                  {loadedSnapshot
                    ? "This saved trip can't be rendered"
                    : "Trip view crashed"}
                </div>
                <div className="mt-1 text-rose-300/80">
                  {loadedSnapshot
                    ? "Its snapshot is from an older version. Re-plan and re-save to refresh it."
                    : err.message}
                </div>
                <button
                  type="button"
                  onClick={reset}
                  className="mt-2 rounded-md bg-rose-800 px-2 py-1 text-xs font-medium text-white hover:bg-rose-700"
                >
                  {loadedSnapshot ? "Drop snapshot & re-plan" : "Dismiss"}
                </button>
              </div>
            )}
          >
            {loadedSnapshot && (
              <SnapshotBanner
                name={loadedSnapshot.name}
                updatedAt={loadedSnapshot.updatedAt}
                onReplan={() => {
                  setLoadedSnapshot(null);
                  planMutation.mutate();
                }}
                replanPending={planMutation.isPending}
              />
            )}
            <TripPlanResult
              plan={displayPlan}
              originLabel={origin?.label ?? ""}
              destLabel={destination?.label ?? ""}
              advice={displayAdvice}
              adviceLoading={adviceMutation.isPending}
              onAnalyze={() => fireAdvice(displayPlan)}
              onPreviewStop={async (stop) => {
                if (!origin || !destination) return null;
                const target = targetSoc.trim() === "" ? undefined : Number(targetSoc);
                const vid = firstVehicle?.rivian_vehicle_id;
                const liveSoc = stateQuery.data?.battery_level_pct;
                const manualSoc = startingSoc.trim() !== "" ? Number(startingSoc) : undefined;
                const soc = manualSoc ?? (typeof liveSoc === "number" && liveSoc > 0 ? liveSoc : undefined);
                try {
                  const hypo = await backend.planTrip({
                    vehicle_id: vid,
                    starting_soc: soc,
                    origin_bearing: 0,
                    target_arrival_soc_percent: target,
                    drive_mode: driveMode || undefined,
                    has_adapter: hasAdapter,
                    pack_kwh:
                      typeof firstVehicle?.pack_kwh === "number" && firstVehicle.pack_kwh > 0
                        ? firstVehicle.pack_kwh
                        : undefined,
                    waypoints: [
                      { latitude: origin.lat, longitude: origin.lon, waypoint_type: "OTHER" },
                      ...extraStops.map((s) => ({
                        latitude: s.lat,
                        longitude: s.lon,
                        waypoint_type: "OTHER",
                      })),
                      { latitude: stop.lat, longitude: stop.lon, waypoint_type: "OTHER" },
                      { latitude: destination.lat, longitude: destination.lon, waypoint_type: "OTHER" },
                    ],
                  });
                  const r0 = displayPlan.Routes?.[0];
                  const r1 = hypo.Routes?.[0];
                  if (!r0 || !r1) return null;
                  const isCharger = (w: { WaypointType: string }) => {
                    const t = w.WaypointType.toLowerCase();
                    return t !== "origin" && t !== "destination" && t !== "waypoint" && t !== "other";
                  };
                  const origStops = r0.Waypoints.filter(isCharger);
                  const newStops = r1.Waypoints.filter(isCharger);
                  // Proximity threshold for "kept" vs "dropped/added".
                  // 25 mi covers reasonable lat/lon jitter Rivian
                  // sometimes shows for the same charging site
                  // across replans (entity IDs aren't always stable).
                  const PROX_MI = 25;
                  const distMi = (
                    a: { Latitude: number; Longitude: number },
                    b: { Latitude: number; Longitude: number },
                  ) => {
                    // Equirectangular approximation; fine at the
                    // 25-mi scale where lat/lon distortion is < 1%.
                    const toRad = (d: number) => (d * Math.PI) / 180;
                    const lat = ((a.Latitude + b.Latitude) / 2) * (Math.PI / 180);
                    const dx = (toRad(a.Longitude) - toRad(b.Longitude)) * Math.cos(lat);
                    const dy = toRad(a.Latitude) - toRad(b.Latitude);
                    return Math.sqrt(dx * dx + dy * dy) * 3958.8;
                  };
                  // Hungarian-flavoured matching but greedy is plenty
                  // for ~5-stop trips: pair each new stop with its
                  // nearest unused original if within threshold.
                  const usedOrig = new Set<number>();
                  let kept = 0;
                  const droppedNames: string[] = [];
                  for (const ns of newStops) {
                    let bestIdx = -1;
                    let bestD = Infinity;
                    for (let i = 0; i < origStops.length; i++) {
                      if (usedOrig.has(i)) continue;
                      const d = distMi(ns, origStops[i]);
                      if (d < bestD) {
                        bestD = d;
                        bestIdx = i;
                      }
                    }
                    if (bestIdx >= 0 && bestD <= PROX_MI) {
                      usedOrig.add(bestIdx);
                      kept++;
                    }
                  }
                  // Original stops with no match in the new plan = dropped.
                  for (let i = 0; i < origStops.length; i++) {
                    if (!usedOrig.has(i)) droppedNames.push(origStops[i].Name || "Stop");
                  }
                  const added = Math.max(0, newStops.length - kept);
                  // Substitution detection: a stop dropped AND the
                  // candidate appears as a charging stop in the new
                  // plan within proximity of the dropped one.
                  let substitutedFor = "";
                  if (droppedNames.length > 0) {
                    const droppedOriginals = origStops.filter((_, i) => !usedOrig.has(i));
                    const candidateNearby = newStops.find(
                      (ns) =>
                        distMi(ns, { Latitude: stop.lat, Longitude: stop.lon }) <= PROX_MI,
                    );
                    if (candidateNearby) {
                      const closestDropped = droppedOriginals
                        .map((d) => ({ d, dist: distMi(candidateNearby, d) }))
                        .sort((a, b) => a.dist - b.dist)[0];
                      if (closestDropped && closestDropped.dist <= 60) {
                        substitutedFor = closestDropped.d.Name || "a planned stop";
                      }
                    }
                  }
                  const c0 = displayPlan.costs?.[0];
                  const c1 = hypo.costs?.[0];
                  return {
                    delta_total_time_sec: r1.TotalTripDurationSec - r0.TotalTripDurationSec,
                    delta_arrival_soc_pct: r1.ArrivalSoC - r0.ArrivalSoC,
                    delta_stop_count: newStops.length - origStops.length,
                    delta_cost: (c1?.dcfc_spend ?? 0) - (c0?.dcfc_spend ?? 0),
                    currency: c1?.currency || c0?.currency || "USD",
                    stops_kept: kept,
                    stops_dropped: droppedNames.length,
                    stops_added: added,
                    substituted_for: substitutedFor || undefined,
                  };
                } catch {
                  return null;
                }
              }}
              onAddStop={(stop) => {
                const labeled = relabelIfHome(stop, home);
                setExtraStops((prev) => {
                  const dup = prev.some(
                    (s) =>
                      Math.abs(s.lat - labeled.lat) < 0.0005 &&
                      Math.abs(s.lon - labeled.lon) < 0.0005,
                  );
                  if (dup) return prev;
                  // Mark the next render's effect to fire a replan.
                  // queueMicrotask would race the React render cycle
                  // (mutationFn would still see the old extraStops);
                  // the autoReplanAfterAddRef flag below threads the
                  // intent through a useEffect so the mutation reads
                  // the post-render closure.
                  autoReplanAfterAddRef.current = true;
                  return [...prev, labeled];
                });
                setOvernightFlags((prev) => [...prev, false]);
              }}
              departureAt={departureAt}
            />
          </ErrorBoundary>
        );
      })()}
    </div>
  );
}

// SAVED_SNAPSHOT_STALE_HOURS is when the "Replan against current
// conditions?" banner switches from informational to amber. Below
// this the snapshot is fresh enough that station availability,
// weather, and ETA haven't meaningfully drifted.
const SAVED_SNAPSHOT_STALE_HOURS = 6;

function SnapshotBanner({
  name,
  updatedAt,
  onReplan,
  replanPending,
}: {
  name: string;
  updatedAt: string;
  onReplan: () => void;
  replanPending: boolean;
}) {
  const ageMs = Date.now() - new Date(updatedAt).getTime();
  const ageH = ageMs / 3_600_000;
  const stale = ageH > SAVED_SNAPSHOT_STALE_HOURS;
  const human =
    ageH < 1
      ? `${Math.max(1, Math.round(ageMs / 60_000))} min ago`
      : ageH < 24
        ? `${Math.round(ageH)}h ago`
        : `${Math.round(ageH / 24)}d ago`;
  return (
    <div
      className={`flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm ${
        stale
          ? "border-amber-900/60 bg-amber-950/30 text-amber-200/90"
          : "border-neutral-800 bg-neutral-900/60 text-neutral-300"
      }`}
    >
      <span>
        Showing saved snapshot <strong className="text-neutral-100">"{name}"</strong>{" "}
        from {human}
        {stale && " — charging stations, weather, and ETA may have drifted."}
      </span>
      <button
        type="button"
        onClick={onReplan}
        disabled={replanPending}
        className="shrink-0 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-xs text-neutral-100 hover:border-neutral-500 disabled:opacity-50"
      >
        {replanPending ? "Re-planning…" : "Re-plan now"}
      </button>
    </div>
  );
}

function SavedTripsCard({
  trips,
  loading,
  activeId,
  canSave,
  saveDisabled,
  onLoad,
  onDelete,
  onSave,
  saveError,
}: {
  trips: SavedTrip[];
  loading: boolean;
  activeId: string | null;
  canSave: boolean;
  saveDisabled: boolean;
  onLoad: (t: SavedTrip) => void;
  onDelete: (id: string) => void;
  onSave: (name: string, id: string | null) => void;
  saveError?: string;
}) {
  const [name, setName] = useState("");
  const active = trips.find((t) => t.id === activeId) ?? null;
  useEffect(() => {
    setName(active?.name ?? "");
  }, [active?.id, active?.name]);
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    if (!n) return;
    // If the typed name matches the active trip, treat as update;
    // otherwise as create-with-this-name (which can 409 — surface as
    // saveError).
    const matched = active && active.name === n ? active.id : null;
    onSave(n, matched);
  };
  return (
    <Card title="Saved trips">
      {loading ? (
        <Spinner />
      ) : trips.length === 0 ? (
        <p className="text-xs text-neutral-500">
          No saved trips yet. Plan a route, then save it below so you can
          re-open or re-plan it later.
        </p>
      ) : (
        <ul className="mb-4 space-y-1.5">
          {trips.map((t) => {
            const isActive = t.id === activeId;
            return (
              <li
                key={t.id}
                className={`flex items-center gap-2 rounded-md border px-3 py-2 text-sm ${
                  isActive
                    ? "border-emerald-700/60 bg-emerald-950/30"
                    : "border-neutral-800 bg-neutral-950/40"
                }`}
              >
                <button
                  type="button"
                  onClick={() => onLoad(t)}
                  className="flex-1 text-left"
                >
                  <span className="text-neutral-100">{t.name}</span>
                  <span className="ml-2 text-xs text-neutral-500">
                    {t.inputs.origin?.label} → {t.inputs.destination?.label}
                  </span>
                </button>
                <button
                  type="button"
                  onClick={() => {
                    if (confirm(`Delete saved trip "${t.name}"?`)) onDelete(t.id);
                  }}
                  className="shrink-0 text-xs text-neutral-500 hover:text-neutral-300"
                >
                  delete
                </button>
              </li>
            );
          })}
        </ul>
      )}
      <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={active ? `Update "${active.name}"` : "Save current trip as…"}
          className="flex-1 min-w-[12rem] rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-100 placeholder-neutral-600 focus:border-neutral-500 focus:outline-none"
        />
        <button
          type="submit"
          disabled={!canSave || saveDisabled || !name.trim()}
          className="rounded-md bg-neutral-800 px-3 py-1.5 text-sm text-neutral-100 hover:bg-neutral-700 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {active && active.name === name.trim() ? "Update" : "Save"}
        </button>
      </form>
      {!canSave && (
        <p className="mt-2 text-xs text-neutral-600">
          Pick a From + To above (and optionally hit Plan trip) before saving.
        </p>
      )}
      {saveError && (
        <p className="mt-2 text-xs text-red-400">{saveError}</p>
      )}
    </Card>
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
  onSaveAsFavorite,
}: {
  heading: string;
  value: Selection;
  onChange: (s: Selection) => void;
  presets: { label: string; lat: number; lon: number }[];
  onSaveAsFavorite?: (s: { lat: number; lon: number; label: string }) => void;
}) {
  return (
    <div className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3">
      <div className="flex items-baseline justify-between gap-3">
        <div className="text-sm">
          <span className="text-neutral-400">{heading}: </span>
          {value ? (
            value.label === "Home" ? (
              <span className="rounded border border-emerald-700 bg-emerald-950/40 px-1.5 py-0.5 text-emerald-300">
                🏠 Home
              </span>
            ) : (
              <span className="text-neutral-100">{value.label}</span>
            )
          ) : (
            <span className="text-neutral-500">— pick a place —</span>
          )}
        </div>
        {value && (
          <div className="flex items-center gap-3 text-xs">
            {onSaveAsFavorite && value.label !== "Home" && (
              <button
                type="button"
                onClick={() => onSaveAsFavorite(value)}
                className="text-amber-400 hover:text-amber-200"
                title="Save this place as a one-tap preset"
              >
                ☆ save
              </button>
            )}
            <button
              type="button"
              onClick={() => onChange(null)}
              className="text-neutral-500 hover:text-neutral-300"
            >
              clear
            </button>
          </div>
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
          {presets.map((p) => {
            // Highlight Home so it stands out from regional
            // presets (TX cities etc.) — Home is the one a user
            // taps most often, so it earns an emerald outline
            // instead of the muted neutral chip.
            const isHome = p.label === "Home";
            return (
              <button
                key={p.label}
                type="button"
                onClick={() => onChange({ lat: p.lat, lon: p.lon, label: p.label })}
                className={
                  isHome
                    ? "rounded-md border border-emerald-700 bg-emerald-950/40 px-2 py-1 text-emerald-300 hover:border-emerald-500 hover:bg-emerald-950"
                    : "rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 hover:border-neutral-500 hover:bg-neutral-800"
                }
              >
                {isHome ? "🏠 " : ""}{p.label}
              </button>
            );
          })}
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

// PreviewResult is the deltas the map's "Preview impact" popup uses
// to show what adding a candidate stop would do BEFORE the user
// commits. Negative values mean "this candidate would improve",
// positive means "would worsen", except for delta_arrival_soc_pct
// where positive = arrives higher (better).
//
// Stop-by-stop comparison fields make Rivian's reoptimization
// visible: if the planner drops an existing stop because the
// candidate provides the same energy on the corridor, the popup
// can call that out instead of just showing "+55 min" with no
// explanation of where the time went.
export type PreviewResult = {
  delta_total_time_sec: number;
  delta_arrival_soc_pct: number;
  delta_stop_count: number;
  delta_cost: number;
  currency: string;
  stops_kept: number;
  stops_dropped: number;
  stops_added: number;
  // Name of an existing planned stop the candidate effectively
  // replaced (existing dropped AND candidate is in the new plan's
  // charging list within proximity). Empty when no substitution
  // happened.
  substituted_for?: string;
};

type StopPreviewer = (
  stop: { lat: number; lon: number; label: string },
) => Promise<PreviewResult | null>;

// ViaStopList renders existing via stops with remove buttons and a
// search field for adding new ones by geocode.
function ViaStopList({
  stops,
  overnightFlags,
  onRemove,
  onAdd,
  onToggleOvernight,
}: {
  stops: NonNullable<Selection>[];
  overnightFlags: boolean[];
  onRemove: (i: number) => void;
  onAdd: StopAdder;
  onToggleOvernight: (i: number, on: boolean) => void;
}) {
  const [adding, setAdding] = useState(false);
  return (
    <div className="space-y-2">
      {stops.map((stop, i) => (
        <div
          key={i}
          className="flex flex-wrap items-center gap-2 rounded-lg border border-neutral-800 bg-neutral-950/40 px-3 py-2 text-sm"
        >
          <span className="shrink-0 text-neutral-500">Via:</span>
          <span className="flex-1 text-neutral-100">{stop.label}</span>
          <label
            className="flex shrink-0 items-center gap-1 text-xs text-neutral-400"
            title="Treat this stop as an overnight: ends one day's drive, starts the next. Assumes 10 h plugged in at 7 kW L2."
          >
            <input
              type="checkbox"
              checked={!!overnightFlags[i]}
              onChange={(e) => onToggleOvernight(i, e.target.checked)}
              className="h-3.5 w-3.5 accent-emerald-500"
            />
            Overnight
          </label>
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
  onPreviewStop,
  departureAt,
}: {
  plan: TripPlan;
  originLabel: string;
  destLabel: string;
  advice?: TripAdvice;
  adviceLoading?: boolean;
  onAnalyze?: () => void;
  onAddStop?: StopAdder;
  onPreviewStop?: StopPreviewer;
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
      <WeatherAdjustmentChip plan={plan} />
      {plan.Routes.map((route, i) => (
        <RouteCard
          key={i}
          route={route}
          index={i}
          allRoutes={plan.Routes}
          originLabel={originLabel}
          destLabel={destLabel}
          onAddStop={onAddStop}
          onPreviewStop={onPreviewStop}
          departureAt={departureAt}
          cost={plan.costs?.[i]}
          primaryCost={plan.costs?.[0]}
          adjustment={i === 0 ? plan.weather_adjustment : undefined}
        />
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

// WeatherAdjustmentChip renders a callout when the rivolt-side
// weather correction predicts the corrected arrival SoC will fall
// below the user's target floor. Rivian's planner doesn't see
// temperature / wind / precip, so cold days and headwind days
// silently turn comfortable plans into stranded ones; this chip is
// the user's warning that they should add a stop or pad the target.
//
// Renders nothing when there's no adjustment (toggle off, fetch
// failed on every leg), or when the adjustment confirms the plan is
// still above target (cold day with extra margin).
function WeatherAdjustmentChip({ plan }: { plan: TripPlan }) {
  const adj = plan.weather_adjustment;
  if (!adj) return null;
  const rivianArrival = plan.Routes[0]?.ArrivalSoC ?? 0;
  // Only fire when WEATHER is what crossed the target line. If Rivian's
  // own plan was already below the target, the existing SoCBelowLimit
  // banner / Rivian-side warning is the right surface — this chip
  // claiming weather is to blame would be misleading. Likewise if the
  // weather correction didn't meaningfully move the number (< 2pp),
  // showing the chip just adds noise.
  if (!adj.below_target) return null;
  if (rivianArrival < adj.target_arrival_soc) return null;
  const delta = rivianArrival - adj.final_arrival_soc;
  if (delta < 2) return null;
  const reasons = (() => {
    // Pick the worst leg and describe its dominant weather factor(s).
    // Profile reasons (wheels/tires/accessories/payload) are
    // trip-wide so they slot in alongside per-leg weather.
    let worst = adj.legs[0];
    adj.legs.forEach((leg) => {
      if (leg.multiplier > worst.multiplier) worst = leg;
    });
    const out: string[] = [];
    if (worst.temp_c != null && worst.temp_c < 10) out.push(`cold (${worst.temp_c.toFixed(0)}°C)`);
    if (worst.temp_c != null && worst.temp_c > 30) out.push(`heat (${worst.temp_c.toFixed(0)}°C)`);
    if (worst.headwind_kph != null && worst.headwind_kph > 10) out.push(`${worst.headwind_kph.toFixed(0)} kph headwind`);
    if (worst.precip_mm != null && worst.precip_mm > 0.5) out.push("rain");
    if (adj.profile_reasons) out.push(...adj.profile_reasons);
    return out;
  })();
  // Title leans on the dominant signal: profile-only correction
  // reads "Your vehicle config..."; pure weather reads "Weather...";
  // both reads as a combined headline.
  const hasProfile = (adj.profile_reasons?.length ?? 0) > 0;
  const hasWeather = adj.legs.some(
    (l) =>
      (l.temp_c != null && (l.temp_c < 10 || l.temp_c > 30)) ||
      (l.headwind_kph != null && l.headwind_kph > 10) ||
      (l.precip_mm != null && l.precip_mm > 0.5),
  );
  const title = hasProfile && hasWeather
    ? "Weather + your config push arrival below target"
    : hasProfile
      ? "Your vehicle config pushes arrival below target"
      : "Weather pushes arrival below your target";
  return (
    <div className="rounded-lg border border-amber-700/50 bg-amber-950/30 p-3 text-sm">
      <div className="font-medium text-amber-200">{title}</div>
      <div className="mt-1 text-amber-300/80">
        Rivian's planner shows{" "}
        <span className="font-semibold text-amber-100">{rivianArrival.toFixed(0)}%</span>{" "}
        at the destination. Correcting for{" "}
        {reasons.length > 0 ? reasons.join(" + ") : "conditions along the route"},
        the realistic arrival is{" "}
        <span className="font-semibold text-amber-100">
          {adj.final_arrival_soc.toFixed(0)}%
        </span>{" "}
        - below your {adj.target_arrival_soc.toFixed(0)}% target. Consider
        adding a stop or padding the target.
      </div>
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

// planForTable trims a verbose plan label down to what fits the
// narrow Cost column. "Pass+ - $7/mo" -> "Pass+". Tesla's "Supercharging
// Membership" is the one outlier where the bare plan word is already
// generic enough that the network name in the Stop column carries
// the meaning - collapse to "Membership" so "Tesla Supercharger -
// X" / "$Y with Supercharging Membership" stops being a stutter.
function planForTable(plan: string): string {
  const short = plan.replace(/\s*[-,].*/, "").trim();
  return short === "Supercharging Membership" ? "Membership" : short;
}

// routeLabel picks the route's name. Simple two-tier scheme:
// Rivian's top pick is "Recommended"; everything after is
// "Alternative N". The descriptive variants ("Faster by 22 min",
// etc.) competed for screen space with the cost line and lost -
// the user can see Distance / Total time / Arrival in the stat
// grid right below the title.
function routeLabel(index: number, allRoutes: TripRoute[]): string {
  if (allRoutes.length <= 1) return "Recommended";
  if (index === 0) return "Recommended";
  return `Alternative ${index}`;
}

// countChargingStops applies the same waypoint-type filter the
// stops table uses: drop origin / destination / waypoint / other.
function countChargingStops(r: TripRoute): number {
  return r.Waypoints.filter((w) => {
    const t = w.WaypointType.toLowerCase();
    return t !== "origin" && t !== "destination" && t !== "waypoint" && t !== "other";
  }).length;
}

// routeDiff builds the "saves $12 · 18 min slower · 1 fewer stop"
// summary line shown under each alternative's title. Each dimension
// has a small noise threshold so a trivially-different route doesn't
// pretend to be meaningfully different. Currency-format the cost
// delta with the route's own currency so EU / GB users see € / £.
function routeDiff(
  route: TripRoute,
  cost: TripCostEstimate | undefined,
  primary: TripRoute,
  primaryCost: TripCostEstimate | undefined,
): string[] {
  const parts: string[] = [];
  const fmtMoney = (v: number) =>
    new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: cost?.currency || primaryCost?.currency || "USD",
      maximumFractionDigits: 0,
    }).format(v);

  const dCost = (primaryCost?.dcfc_spend ?? 0) - (cost?.dcfc_spend ?? 0);
  if (Math.abs(dCost) >= 0.5) {
    parts.push(dCost > 0 ? `saves ${fmtMoney(dCost)}` : `+${fmtMoney(Math.abs(dCost))}`);
  }
  const dTime = primary.TotalTripDurationSec - route.TotalTripDurationSec;
  if (Math.abs(dTime) >= 120) {
    parts.push(
      dTime > 0 ? `${formatDuration(dTime)} faster` : `${formatDuration(-dTime)} slower`,
    );
  }
  const dStops = countChargingStops(primary) - countChargingStops(route);
  if (dStops !== 0) {
    parts.push(
      dStops > 0
        ? `${dStops} fewer stop${dStops === 1 ? "" : "s"}`
        : `${-dStops} more stop${dStops === -1 ? "" : "s"}`,
    );
  }
  const dArr = route.ArrivalSoC - primary.ArrivalSoC;
  if (Math.abs(dArr) >= 2) {
    parts.push(
      dArr > 0
        ? `arrives ${dArr.toFixed(0)}% higher`
        : `arrives ${Math.abs(dArr).toFixed(0)}% lower`,
    );
  }
  return parts;
}

function RouteCard({
  route,
  index,
  allRoutes,
  originLabel,
  destLabel,
  onAddStop,
  onPreviewStop,
  departureAt,
  cost,
  primaryCost,
  adjustment,
}: {
  route: TripRoute;
  index: number;
  allRoutes: TripRoute[];
  originLabel: string;
  destLabel: string;
  onAddStop?: StopAdder;
  onPreviewStop?: StopPreviewer;
  departureAt?: string;
  cost?: TripCostEstimate;
  primaryCost?: TripCostEstimate;
  adjustment?: TripPlan["weather_adjustment"];
}) {
  // Rivian's planTrip2 returns waypointType in lowercase ("origin" /
  // "destination" / "waypoint"); compare case-insensitively so the
  // table renders correctly regardless of casing.
  const wpType = (w: { WaypointType: string }) => w.WaypointType.toLowerCase();
  // Track each charging stop's original index in route.Waypoints so
  // distanceCell can look up the predecessor for leg-distance math.
  const chargingWithIdx = route.Waypoints
    .map((w, i) => ({ w, i }))
    .filter(({ w }) => {
      const t = wpType(w);
      return t !== "origin" && t !== "destination" && t !== "waypoint" && t !== "other";
    });
  const charging = chargingWithIdx.map(({ w }) => w);
  const originIdx = route.Waypoints.findIndex((w) => wpType(w) === "origin");
  const destIdx = (() => {
    // Avoid findLastIndex - target lib predates ES2023. Walk in
    // reverse so multi-segment routes still find the trailing
    // destination row.
    for (let i = route.Waypoints.length - 1; i >= 0; i--) {
      if (wpType(route.Waypoints[i]) === "destination") return i;
    }
    return -1;
  })();
  const origin = originIdx >= 0 ? route.Waypoints[originIdx] : undefined;
  const dest = destIdx >= 0 ? route.Waypoints[destIdx] : undefined;
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
  const fmtCurrency = (v: number) =>
    new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: cost?.currency || "USD",
    }).format(v);
  // Per-stop $ keyed by the same index the stops table walks. The
  // breakdown is server-computed from the same Waypoints array, so
  // index alignment is safe.
  const stopBreakdown = cost?.breakdown ?? [];
  const totalGuest = cost?.dcfc_spend ?? 0;
  const totalUser = cost?.dcfc_spend_user_member ?? 0;
  const userSavings = totalGuest - totalUser;
  const gasEquiv = cost?.gas_equivalent ?? 0;
  // Bidirectional selection between the stops table and the map.
  // Holds the index in route.Waypoints of the highlighted stop, or
  // null for "nothing selected". Reset to null when the route prop
  // changes so an old selection doesn't leak across plans.
  const [selectedStop, setSelectedStop] = useState<number | null>(null);
  useEffect(() => { setSelectedStop(null); }, [route]);
  const toggleStop = (idx: number) =>
    setSelectedStop((prev) => (prev === idx ? null : idx));
  // Distance helpers. cumulative is route.Waypoints[i].DistanceFromOriginMeters;
  // leg = current - previous. Hidden when Rivian didn't populate the
  // field (legacy plans). All values converted to miles for display.
  const haveDistances = route.Waypoints.some((w) => (w.DistanceFromOriginMeters ?? 0) > 0);
  const mFmt = (m: number) => `${(m / 1609.344).toFixed(0)} mi`;
  const distanceCell = (idx: number) => {
    const w = route.Waypoints[idx];
    const cum = w?.DistanceFromOriginMeters ?? 0;
    if (idx === 0) {
      return <td className="px-2 py-2 font-mono text-xs text-neutral-500">0 mi</td>;
    }
    const prev = route.Waypoints[idx - 1];
    const leg = cum - (prev?.DistanceFromOriginMeters ?? 0);
    return (
      <td className="px-2 py-2 font-mono text-neutral-200">
        {leg > 0 ? `+${mFmt(leg)}` : "—"}
        {cum > 0 && (
          <div className="text-xs text-neutral-500">{mFmt(cum)} total</div>
        )}
      </td>
    );
  };
  const label = routeLabel(index, allRoutes);
  // Title is JSX so the member price can render in green inline
  // without a verbal "with Membership" suffix - color carries the
  // meaning. Falls back to a single number when no savings exist.
  const title = (
    <span className="flex flex-wrap items-baseline gap-x-2">
      <span>{label}</span>
      {totalGuest > 0 && (
        <span className="text-neutral-400">
          · <span
            className="text-neutral-200 font-mono cursor-help"
            title="Walk-up DCFC cost: every fast-charging stop on this route at the network's guest rate."
          >{fmtCurrency(totalGuest)}</span>
          {userSavings > 0.5 && (
            <>
              {" / "}
              <span
                className="text-emerald-400 font-mono cursor-help"
                title="With your active memberships (Settings → Charging networks)."
              >{fmtCurrency(totalUser)}</span>
            </>
          )}
          {gasEquiv > 0.5 && (
            <>
              {" / "}
              <span
                className="text-red-700 font-mono cursor-help"
                title={`Same distance in a ${cost?.gas_mpg?.toFixed(0) ?? "20"} MPG ICE at ${fmtCurrency(cost?.gas_price_per_gallon ?? 0)}/gal (Settings → Trip planner defaults).`}
              >{fmtCurrency(gasEquiv)}</span>
            </>
          )}
        </span>
      )}
      {!route.DestinationReached && (
        <span className="text-amber-400">· destination unreachable</span>
      )}
    </span>
  );
  return (
    <Card title={title}>
      {index > 0 && allRoutes.length > 1 && (() => {
        // Alternative comparison line. Renders below the title so the
        // user can see at a glance how this route diverges from
        // Recommended (route 0) - "saves $12 · 18 min slower · 1
        // fewer stop". Dropped entirely when no dimension crossed
        // the noise threshold.
        const parts = routeDiff(route, cost, allRoutes[0], primaryCost);
        if (parts.length === 0) return null;
        return (
          <div className="-mt-1 mb-3 text-xs text-neutral-400">
            vs Recommended: {parts.join(" · ")}
          </div>
        );
      })()}
      <div className="mb-4">
        <Suspense fallback={<div className="h-80 animate-pulse rounded-lg border border-neutral-800 bg-neutral-900/50" />}>
          <TripRouteMap
            route={route}
            onAddStop={onAddStop}
            onPreviewStop={onPreviewStop}
            selectedIdx={selectedStop}
            onSelectStop={(idx) => setSelectedStop(idx)}
            departureAt={departureAt}
          />
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
              // Prefer the corrected arrival SoC when the
              // adjustment pipeline ran (weather and/or profile);
              // it's the realistic number the user will actually
              // see at the destination, not Rivian's
              // weather/config-blind projection.
              const arrSoC = adjustment ? adjustment.final_arrival_soc : route.ArrivalSoC;
              const soc = arrSoC > 0 ? `${arrSoC.toFixed(0)}%` : "—";
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
                {haveDistances && <th className="px-2 py-2">Distance</th>}
                <th className="px-2 py-2">Arrive</th>
                <th className="px-2 py-2">Depart</th>
                <th className="px-2 py-2">Charge</th>
                <th className="px-2 py-2">Max kW</th>
                {stopBreakdown.length > 0 && <th className="px-2 py-2 text-right">Cost</th>}
                <th className="px-2 py-2">Adapter?</th>
              </tr>
            </thead>
            <tbody>
              {origin && (
                <tr
                  onClick={() => toggleStop(originIdx)}
                  className={`border-b border-neutral-900 text-neutral-400 cursor-pointer transition-colors ${
                    selectedStop === originIdx ? "bg-emerald-900/20" : "hover:bg-neutral-900/40"
                  }`}
                >
                  <td className="px-2 py-2">S</td>
                  <td className="px-2 py-2">{originLabel || origin.Name || "Origin"}</td>
                  {haveDistances && distanceCell(originIdx)}
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2 font-mono text-neutral-100">
                    {formatWaypointTime(origin.DepartureTimeUTC, timeShiftMs) && (
                      <div className="text-xs text-neutral-500">{formatWaypointTime(origin.DepartureTimeUTC, timeShiftMs)}</div>
                    )}
                    {origin.DepartureSoC > 0 ? `${origin.DepartureSoC.toFixed(0)}%` : "—"}
                  </td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  {stopBreakdown.length > 0 && <td className="px-2 py-2"></td>}
                  <td className="px-2 py-2"></td>
                </tr>
              )}
              {chargingWithIdx.map(({ w, i: wpIdx }, j) => {
                const stopCost = stopBreakdown[j];
                return (
                  <tr
                    key={j}
                    onClick={() => toggleStop(wpIdx)}
                    className={`border-b border-neutral-900 cursor-pointer transition-colors ${
                      selectedStop === wpIdx ? "bg-emerald-900/20" : "hover:bg-neutral-900/40"
                    }`}
                  >
                    <td className="px-2 py-2 text-neutral-500">{j + 1}</td>
                    <td className="px-2 py-2">{w.Name || `(${w.Latitude.toFixed(3)}, ${w.Longitude.toFixed(3)})`}</td>
                    {haveDistances && distanceCell(wpIdx)}
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
                    {stopBreakdown.length > 0 && (
                      <td className="px-2 py-2 font-mono text-right">
                        {stopCost ? (
                          <>
                            <div>{fmtCurrency(stopCost.energy_kwh * stopCost.guest_rate)}</div>
                            {stopCost.user_rate < stopCost.guest_rate ? (
                              // Membership active: bare green
                              // dollar; plan name in title for
                              // hover detail. Color codes the
                              // meaning so the cell stays compact.
                              <div
                                className="text-xs text-emerald-400/80 cursor-help"
                                title={`With ${planForTable(stopCost.member_plan ?? "Membership")}`}
                              >
                                {fmtCurrency(stopCost.energy_kwh * stopCost.user_rate)}
                              </div>
                            ) : stopCost.all_member_rate < stopCost.guest_rate ? (
                              // Membership available but not activated:
                              // muted gray with the upsell on hover.
                              <div
                                className="text-xs text-neutral-500 cursor-help"
                                title={`With ${planForTable(stopCost.member_plan ?? "Membership")} (you'd save here)`}
                              >
                                {fmtCurrency(stopCost.energy_kwh * stopCost.all_member_rate)}
                              </div>
                            ) : null}
                          </>
                        ) : (
                          "—"
                        )}
                      </td>
                    )}
                    <td className="px-2 py-2">
                      {w.AdapterRequired && /\bTesla\b|\[Tesla\]/i.test(w.Name) ? "yes" : ""}
                    </td>
                  </tr>
                );
              })}
              {dest && (
                <tr
                  onClick={() => toggleStop(destIdx)}
                  className={`border-b border-neutral-900 text-neutral-400 cursor-pointer transition-colors ${
                    selectedStop === destIdx ? "bg-emerald-900/20" : "hover:bg-neutral-900/40"
                  }`}
                >
                  <td className="px-2 py-2">E</td>
                  <td className="px-2 py-2">{destLabel || dest.Name || "Destination"}</td>
                  {haveDistances && distanceCell(destIdx)}
                  <td className="px-2 py-2 font-mono text-neutral-100">
                    {formatWaypointTime(dest.ArrivalTimeUTC, timeShiftMs) && (
                      <div className="text-xs text-neutral-500">{formatWaypointTime(dest.ArrivalTimeUTC, timeShiftMs)}</div>
                    )}
                    {dest.ArrivalSoC > 0 ? `${dest.ArrivalSoC.toFixed(0)}%` : "—"}
                  </td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  {stopBreakdown.length > 0 && (
                    <td className="px-2 py-2 font-mono text-right text-neutral-200">
                      {fmtCurrency(totalGuest)}
                      {userSavings > 0.5 && (
                        <div
                          className="text-xs text-emerald-400/80 cursor-help"
                          title="With your active memberships"
                        >
                          {fmtCurrency(totalUser)}
                        </div>
                      )}
                      {userSavings <= 0.5 &&
                        (cost?.dcfc_spend_all_member ?? 0) > 0 &&
                        cost!.dcfc_spend_all_member < totalGuest - 0.5 && (
                          <div
                            className="text-xs text-neutral-500 cursor-help"
                            title="If you held every applicable membership"
                          >
                            {fmtCurrency(cost!.dcfc_spend_all_member)}
                          </div>
                        )}
                    </td>
                  )}
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
  // Collapsed-by-default once advice arrives: keeps the card a single
  // line so the map below doesn't jump when the LLM response lands.
  // User opts in to the full breakdown by tapping the "ready" row.
  const [expanded, setExpanded] = useState(false);
  // Reset when a new plan's advice replaces the prior one - the
  // user should re-opt-in to view each fresh analysis. Keyed on
  // headline because object identity isn't stable across renders.
  useEffect(() => {
    setExpanded(false);
  }, [advice?.headline]);

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
      {advice && !expanded && (
        <button
          type="button"
          onClick={() => setExpanded(true)}
          className="flex items-center gap-2 text-sm text-left rounded-md w-full hover:bg-neutral-900/50 transition-colors py-1 -my-1 px-1"
        >
          <span className="inline-flex h-4 w-4 items-center justify-center rounded-full bg-emerald-700/40 text-emerald-300 text-[10px]">✓</span>
          <span className="text-neutral-200">Analysis ready</span>
          {advice.headline && (
            <span className="text-neutral-500 truncate">- {advice.headline}</span>
          )}
          <span className="ml-auto text-xs text-neutral-500">Tap to view</span>
        </button>
      )}
      {advice && expanded && (
        <div className="space-y-4">
          {advice.headline && (
            <p className="font-medium text-neutral-100">{advice.headline}</p>
          )}
          <AdviceCostStrip cost={advice.cost_estimate} />
          <AdviceSection label="Cost" items={advice.cost} accent="emerald" />
          <AdviceSection label="Efficiency" items={advice.efficiency} accent="sky" />
          <AdviceSection label="Weather" items={advice.weather} accent="cyan" />
          <AdviceSection label="Vehicle" items={advice.vehicle} accent="amber" />
          <div className="flex items-center justify-between pt-1 border-t border-neutral-800/60">
            {advice.model ? (
              <p className="text-xs text-neutral-600">{advice.model}</p>
            ) : <span />}
            <button
              type="button"
              onClick={() => setExpanded(false)}
              className="text-xs text-neutral-500 hover:text-neutral-300"
            >
              Collapse
            </button>
          </div>
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
  // Totals + per-network breakdown moved into each RouteCard. This
  // strip survives only as the trip-wide "you could save $X more by
  // joining every applicable plan" hint, which is a membership
  // upsell and naturally belongs in the analysis card. Render
  // nothing when nothing is actionable.
  const maxExtra = cost.dcfc_spend_user_member - cost.dcfc_spend_all_member;
  if (cost.dcfc_spend <= 0 || maxExtra < 0.5) return null;
  const fmt = (v: number) =>
    new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: cost.currency || "USD",
    }).format(v);
  return (
    <div className="rounded-md border border-amber-700/40 bg-amber-950/20 px-3 py-2 text-xs text-amber-300/80">
      You could save another{" "}
      <span className="font-semibold text-amber-100">{fmt(maxExtra)}</span> on
      this trip by joining the charging-network memberships you don't have yet
      (lowest possible cost: {fmt(cost.dcfc_spend_all_member)}).
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

// LONG_DAY_THRESHOLD_MI / LONG_DAY_THRESHOLD_HR drive the "long day"
// chip on each day card. Picked from the road-trip rule of thumb that
// driving more than ~10 hours or ~600 miles in a single day is fatigue
// territory.
const LONG_DAY_THRESHOLD_MI = 600;
const LONG_DAY_THRESHOLD_HR = 9;

// MultidayResult renders a multi-day trip as a stack of full per-day
// RouteCards (with map + stops table) plus a trip-total header. Each
// day carries its own departure/arrival SoC pill, a long-day warning
// when distance or time exceeds the threshold, and an overnight
// footer showing the L2 math. AI advice + saved trips don't flow
// through this path yet.
function MultidayResult({
  response,
  originLabel,
  destLabel,
  departureAt,
}: {
  response: TripPlanMultidayResponse;
  originLabel: string;
  destLabel: string;
  departureAt?: string;
}) {
  const totalMiles = response.total.distance_meters / 1609.344;
  const totalHrs = response.total.drive_duration_sec / 3600;
  const chargeMin = Math.round(response.total.charging_duration_sec / 60);
  return (
    <Card title={`${response.days.length}-day trip: ${originLabel || "Origin"} → ${destLabel || "Destination"}`}>
      <div className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label="Days" value={String(response.days.length)} />
        <Stat label="Total distance" value={`${totalMiles.toFixed(0)} mi`} />
        <Stat label="Drive time" value={`${totalHrs.toFixed(1)} h`} />
        <Stat label="Charging" value={`${chargeMin} min`} />
      </div>
      <div className="space-y-4">
        {response.days.map((d, i) => {
          const r = d.plan.Routes?.[0];
          // Per-day origin label = previous day's overnight name, or
          // the trip origin on Day 1. Destination label = this day's
          // overnight name, or the trip destination on the last day.
          const dayOrigin = i === 0
            ? (originLabel || "Origin")
            : (response.days[i - 1].overnight?.name || `Overnight ${i}`);
          const dayDest = d.overnight?.name
            ?? (i === response.days.length - 1 ? (destLabel || "Destination") : `Overnight ${i + 1}`);
          const miles = r ? r.TotalDriveDistanceMeters / 1609.344 : 0;
          const hrs = r ? r.TotalDriveDurationSec / 3600 : 0;
          const isLongDay = miles > LONG_DAY_THRESHOLD_MI || hrs > LONG_DAY_THRESHOLD_HR;
          return (
            <div key={d.index} className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3">
              <div className="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
                <span className="rounded bg-emerald-900/40 px-1.5 py-0.5 text-xs font-semibold text-emerald-200">
                  Day {d.index}
                </span>
                <span className="text-sm text-neutral-300">
                  {dayOrigin} → {dayDest}
                </span>
                <span className="text-xs text-neutral-500">
                  start <span className="text-neutral-300 font-mono">{d.departure_soc.toFixed(0)}%</span>
                  {" → "}
                  arrive <span className="text-neutral-300 font-mono">{d.arrival_soc.toFixed(0)}%</span>
                </span>
                {isLongDay && (
                  <span
                    className="rounded bg-amber-900/40 px-1.5 py-0.5 text-xs text-amber-300"
                    title={`Long day: ${miles.toFixed(0)} mi / ${hrs.toFixed(1)} h drive. Consider splitting into two days.`}
                  >
                    long day
                  </span>
                )}
              </div>
              {r ? (
                <Suspense
                  fallback={
                    <div className="h-80 animate-pulse rounded-lg border border-neutral-800 bg-neutral-900/50" />
                  }
                >
                  <RouteCard
                    route={r}
                    index={0}
                    allRoutes={d.plan.Routes ?? [r]}
                    originLabel={dayOrigin}
                    destLabel={dayDest}
                    departureAt={departureAt}
                    cost={d.costs?.[0]}
                    primaryCost={d.costs?.[0]}
                  />
                </Suspense>
              ) : (
                <div className="text-xs text-rose-300">
                  Rivian returned no route for this day.
                </div>
              )}
              {d.overnight && (
                <div className="mt-3 text-xs text-neutral-400">
                  Overnight{" "}
                  {d.overnight.name ? <span className="text-neutral-300">at {d.overnight.name}</span> : null}
                  {" · "}
                  {d.overnight.l2_kw > 0 ? (
                    <>
                      +{d.overnight.added_kwh.toFixed(0)} kWh @ {d.overnight.l2_kw} kW
                      {" · "}
                      depart{" "}
                      <span className="text-emerald-400 font-mono">
                        {d.overnight.post_charge_soc_pct.toFixed(0)}%
                      </span>
                      {d.overnight.capped && (
                        <span className="text-amber-400" title="Hit max overnight SoC cap">
                          {" "}
                          (capped)
                        </span>
                      )}
                    </>
                  ) : (
                    <>no L2 — carry {d.arrival_soc.toFixed(0)}% to next day</>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </Card>
  );
}
