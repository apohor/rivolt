import { noRoutePrefs, routePrefsFromInputs, routePrefsWire, type RoutePrefs } from "./routePrefs";
import { buildDays, stopKey, type OvernightOptions } from "./days";
import { useEffect, useRef, useState } from "react";
import { formatRouteSummary, useRouteBarState } from "./routeBar";
import { forVehicle } from "../lib/vehicleSamples";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type PlannerFavorite, type SavedTrip, type TripAdvice, type TripPlan } from "../lib/api";
import { useAIEnabled } from "../lib/config";
import { DriveMode, Selection, collectInputs, haversineMeters, normalizeDriveMode, readLastTrip, relabelIfHome, writeLastTrip } from "./plannerHelpers";

// usePlanner: the planner's state, queries, mutations and handlers, so TripPlanPage is layout.
export function usePlanner() {
  // From, to, departure and starting SoC: one hook shared by the classic form
  // and the map-first route bar.
  const routeBar = useRouteBarState();
  const { origin, destination, setOrigin, setDestination } = routeBar;
  const [extraStops, setExtraStops] = useState<NonNullable<Selection>[]>([]);
  // Parallel array to extraStops: true marks that stop as an overnight
  // (ends one day, starts the next). Any true value flips planning to
  // the multi-day endpoint. Defaults baked: 10h parked, 7kW L2.
  const [overnightFlags, setOvernightFlags] = useState<boolean[]>([]);
  // Overnight settings: leave time next morning (HH:MM, local), charger kW at
  // the stop, and the charge cap. These three are the starting values for a new
  // overnight (the last one edited); each overnight then keeps its own in
  // overnightOpts, keyed by its stop.
  const [maxOvernightSoCPct, setMaxOvernightSoCPct] = useState<number>(80);
  const [overnightDepartureLocal, setOvernightDepartureLocal] = useState<string>("08:00");
  const [overnightL2Kw, setOvernightL2Kw] = useState<number>(7);
  const [overnightOpts, setOvernightOpts] = useState<Record<string, OvernightOptions>>({});
  const optsFor = (s: { lat: number; lon: number }): OvernightOptions =>
    overnightOpts[stopKey(s)] ?? { l2Kw: overnightL2Kw, until: overnightDepartureLocal, maxSocPct: maxOvernightSoCPct };
  // remember: the edit also becomes the starting value for the next overnight. A hotel's listed
  // charger belongs to that hotel only, so picking one does not.
  const updateOvernight = (s: { lat: number; lon: number }, patch: Partial<OvernightOptions>, remember = true) => {
    const next = { ...optsFor(s), ...patch };
    // Freeze the other nights at what they show now: the starting values below
    // follow the last edit, and an untouched night must not follow them.
    setOvernightOpts((prev) => {
      const out = { ...prev };
      extraStops.forEach((st, i) => {
        if (overnightFlags[i] && !out[stopKey(st)]) out[stopKey(st)] = optsFor(st);
      });
      out[stopKey(s)] = next;
      return out;
    });
    if (!remember) return;
    setOvernightL2Kw(next.l2Kw);
    setOvernightDepartureLocal(next.until);
    setMaxOvernightSoCPct(next.maxSocPct);
  };
  const [targetSoc, setTargetSoc] = useState<string>("20");
  // Empty = auto from live vehicle state; user can override.
  const { startingSoc, setStartingSoc } = routeBar;
  const [driveMode, setDriveMode] = useState<DriveMode>("EVERYDAY");
  const [hasAdapter, setHasAdapter] = useState<boolean>(false);
  const [routePrefs, setRoutePrefs] = useState<RoutePrefs>(noRoutePrefs);
  // departureAt is a datetime-local string (local time, no timezone).
  // Empty = depart now; Rivian's planner always plans from "now", so
  // we store this and shift displayed waypoint times client-side.
  const { departureAt, setDepartureAt } = routeBar;
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
    queryKey: ["samplesLatestForPlanner", firstVehicle?.rivian_vehicle_id],
    // /api/samples is user-scoped: keep the planner vehicle's rows so a
    // second truck's last fix is never used as the start position.
    queryFn: () =>
      backend
        .samples(
          new Date(Date.now() - 6 * 60 * 60 * 1000),
          5000,
          new Date(Date.now() + 60_000),
          firstVehicle?.rivian_vehicle_id,
        )
        .then((r) => forVehicle(r, firstVehicle?.rivian_vehicle_id)),
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
    if (last.routePrefs) setRoutePrefs({ ...noRoutePrefs, ...last.routePrefs });
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
        route_preferences: routePrefsWire(routePrefs),
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
          routePrefs,
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
      const liveSoc = stateQuery.data?.battery_level_pct;
      const manualSoc = startingSoc.trim() !== "" ? Number(startingSoc) : undefined;
      const soc = manualSoc ?? (typeof liveSoc === "number" && liveSoc > 0 ? liveSoc : undefined);
      const target = targetSoc.trim() === "" ? undefined : Number(targetSoc);
      const vid = firstVehicle?.rivian_vehicle_id;
      return backend.planTripMultiday({
        ...(vid ? { vehicle_id: vid } : {}),
        ...(soc ? { starting_soc: soc } : {}),
        ...(firstVehicle?.pack_kwh ? { pack_kwh: firstVehicle.pack_kwh } : {}),
        start_at: departureAt ? new Date(departureAt).toISOString() : new Date().toISOString(),
        tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
        drive_mode: driveMode || undefined,
        has_adapter: hasAdapter,
        route_preferences: routePrefsWire(routePrefs),
        target_arrival_soc_percent: target,
        origin: { lat: origin.lat, lon: origin.lon, label: origin.label },
        days: buildDays(extraStops, overnightFlags, destination, optsFor),
        max_overnight_soc_pct: maxOvernightSoCPct,
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
      const inputs = collectInputs({
        origin,
        destination,
        extraStops,
        overnightFlags,
        maxOvernightSoCPct,
        overnightL2Kw,
        overnightDepartureLocal,
        overnightSettings: extraStops
          .filter((_, i) => overnightFlags[i])
          .map((st) => {
            const o = optsFor(st);
            return { lat: st.lat, lon: st.lon, until: o.until, l2_kw: o.l2Kw, max_soc_pct: o.maxSocPct };
          }),
        targetSoc,
        startingSoc,
        driveMode,
        hasAdapter,
        departureAt,
        routePrefs,
      });
      // Multi-day plans aren't snapshotted yet (orchestrator response
      // shape doesn't match the single-day TripPlan stored on saved
      // trips); reopening re-plans against current Rivian state.
      const isMultiday = !!inputs.overnight_flags?.some(Boolean);
      const body = {
        name: vars.name,
        inputs,
        plan: isMultiday ? undefined : plan,
        advice: isMultiday ? undefined : advice,
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
    const stops = i.extra_stops ?? [];
    setExtraStops(stops);
    // Restore overnight flags if saved (multi-day trip); else zero them
    // to the new stop count. Snap saved length to current stops length
    // in case the saved payload is out of sync.
    if (Array.isArray(i.overnight_flags)) {
      const flags = stops.map((_, idx) => !!i.overnight_flags?.[idx]);
      setOvernightFlags(flags);
    } else {
      setOvernightFlags(stops.map(() => false));
    }
    if (typeof i.max_overnight_soc_pct === "number") setMaxOvernightSoCPct(i.max_overnight_soc_pct);
    if (typeof i.overnight_l2_kw === "number") setOvernightL2Kw(i.overnight_l2_kw);
    setOvernightOpts(
      Object.fromEntries(
        (i.overnight_settings ?? []).map((o) => [stopKey(o), { l2Kw: o.l2_kw, until: o.until, maxSocPct: o.max_soc_pct }]),
      ),
    );
    if (typeof i.overnight_departure_local === "string") setOvernightDepartureLocal(i.overnight_departure_local);
    if (typeof i.target_soc === "string") setTargetSoc(i.target_soc);
    if (typeof i.starting_soc === "string") setStartingSoc(i.starting_soc);
    if (typeof i.drive_mode === "string") setDriveMode(normalizeDriveMode(i.drive_mode));
    if (typeof i.has_adapter === "boolean") setHasAdapter(i.has_adapter);
    setRoutePrefs(routePrefsFromInputs(i));
    if (typeof i.departure_at === "string") setDepartureAt(i.departure_at);
    setActiveTripId(t.id);
    planMutation.reset();
    adviceMutation.reset();
    multidayMutation.reset();
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

  // The plan screen in pieces: today's order (form, Plan vs actual, saved trips, result)
  // for everyone; in the ui_theme_next layer the result follows the form in the DOM
  // (so Tab and screen readers reach it first), then Plan vs actual and saved trips.
  const hasResult = !!(loadedSnapshot?.plan ?? planMutation.data ?? multidayMutation.data);
  const routeSummary = formatRouteSummary(routeBar);
  const resultKey = loadedSnapshot?.plan ?? planMutation.data ?? multidayMutation.data ?? null;
  return {
    routeBar,
    origin,
    destination,
    setOrigin,
    setDestination,
    extraStops,
    setExtraStops,
    overnightFlags,
    setOvernightFlags,
    maxOvernightSoCPct,
    setMaxOvernightSoCPct,
    overnightL2Kw,
    setOvernightL2Kw,
    optsFor,
    updateOvernight,
    overnightDepartureLocal,
    setOvernightDepartureLocal,
    targetSoc,
    setTargetSoc,
    startingSoc,
    setStartingSoc,
    driveMode,
    setDriveMode,
    hasAdapter,
    setHasAdapter,
    routePrefs,
    setRoutePrefs,
    departureAt,
    setDepartureAt,
    aiEnabled,
    ownedQuery,
    firstVehicle,
    stateQuery,
    profileQuery,
    stateLat,
    stateLon,
    stateHasFix,
    samplesQuery,
    currentPosition,
    qcEarly,
    homeQuery,
    home,
    favoritesQuery,
    favorites,
    favoritesMutation,
    saveAsFavorite,
    lastTripApplied,
    setLastTripApplied,
    prefsQuery,
    prefsApplied,
    setPrefsApplied,
    autoReplanAfterAddRef,
    planMutation,
    adviceMutation,
    hasOvernightStops,
    multidayMutation,
    qc,
    savedTripsQuery,
    loadedSnapshot,
    setLoadedSnapshot,
    activeTripId,
    setActiveTripId,
    saveTripMutation,
    deleteTripMutation,
    loadSavedTrip,
    fireAdvice,
    onSubmit,
    hasResult,
    routeSummary,
    resultKey,
  };
}
