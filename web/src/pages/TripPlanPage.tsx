import { comparableSpend } from "../lib/planCost";
import { type OvernightOptions } from "../plan/days";
import { DEFAULT_OVERNIGHT_KW, HOOKUPS, type HookupId, KW_MAX, KW_MIN, clampKw, hookupById, hookupForKw, hookupNote } from "../plan/hookups";
import { useEffect, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import { PlanTabPanel, PlanTabs, usePlanTab, type PlanTabSpec } from "../plan/PlanTabs";
import { usePlanner } from "../plan/usePlanner";
import { planErrorCopy, relabelIfHome, type Selection } from "../plan/plannerHelpers";
import { RouteBar } from "../plan/RouteBarPanel";
import { countNonDefaultOptions, useRouteBarCollapse } from "../plan/routeBar";
import { PlanSelectionProvider } from "../plan/selection";
import { ConnectHint } from "../components/DemoBanner";
import { appPath, inDemo } from "../lib/demo";
import { useQuery } from "@tanstack/react-query";
import { PlanVsActualSection } from "../components/PlanVsActual";
import { backend, vehicleNeedsTeslaAdapter, type DemoTripPreset, type GeocodeResult, type SavedMultidayPlan, type SavedTrip, type TripPlan } from "../lib/api";
import { Card, ErrorBoundary, ErrorBox, Spinner } from "../components/ui";
import ConnectRivianPrompt from "../components/ConnectRivianPrompt";

import { AvoidToggles } from "../plan/AvoidToggles";
import { StopAdder } from "../plan/tripTypes";
import { multiDayFirst, shortPlace, tripSubtitle, tripTag } from "../plan/tripSubtitle";
import { isAIOff, useAIConsent } from "../lib/aiConsent";
import { SaveTripBar } from "../plan/SaveTripBar";
import { MultidayResult } from "../plan/MultidayResult";
import { ReplanFromHere } from "../plan/ReplanFromHere";
import { TripPlanResult } from "../plan/TripPlanResult";


// Preset destinations come from the user's saved favorites
// (settings.planner.favorites) now; the planner used to ship a
// hardcoded Texas list but that's wrong as soon as a user lives
// anywhere else.

export default function TripPlanPage() {
  const { routeBar, origin, destination, setOrigin, setDestination, extraStops, setExtraStops, overnightFlags, setOvernightFlags, optsFor, updateOvernight, targetSoc, setTargetSoc, startingSoc, setStartingSoc, driveMode, setDriveMode, hasAdapter, setHasAdapter, routePrefs, setRoutePrefs, departureAt, setDepartureAt, firstVehicle, stateQuery, profileQuery, currentPosition, home, favorites, saveAsFavorite, autoReplanAfterAddRef, planMutation, adviceMutation, hasOvernightStops, multidayMutation, savedTripsQuery, loadedSnapshot, setLoadedSnapshot, multiday, multidaySnapshot, setMultidaySnapshot, replanBase, activeTripId, saveTripMutation, deleteTripMutation, loadSavedTrip, fireAdvice, aiEnabled, onSubmit, hasResult, routeSummary, resultKey } = usePlanner();
  const [planTab, setPlanTab] = usePlanTab();
  const aiConsent = useAIConsent();
  // When a plan arrives, bring it into view. Phones: the map (or the multi-day result) up under the top bar so
  // it fills the space above the sheet's peek. Wide screens: only when the result starts well below the fold
  // (a multi-day trip under a tall form); a map or a result beside the form is already in sight. Smooth unless
  // the user prefers reduced motion; focus is not moved (the results announcement keeps it).
  useEffect(() => {
    if (!resultKey || planTab !== "planner") return;
    const wide = window.innerWidth >= 1024;
    let r2 = 0;
    const r1 = requestAnimationFrame(() => {
      r2 = requestAnimationFrame(() => {
        const el = document.querySelector("[data-plan-area='map']:not([data-empty]), [data-plan-wide]");
        if (!el) return;
        const rect = el.getBoundingClientRect();
        if (wide && rect.top < window.innerHeight * 0.7) return;
        const bar = document.querySelector("header.sticky")?.getBoundingClientRect().height ?? 48;
        const top = rect.top + window.scrollY - bar - 8;
        const reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
        window.scrollTo({ top: Math.max(0, top), behavior: reduce ? "auto" : "smooth" });
      });
    });
    return () => {
      cancelAnimationFrame(r1);
      cancelAnimationFrame(r2);
    };
  }, [resultKey, planTab]);
  // The form's pieces, laid out by the map-first route bar (RouteBar).
  const departureEl = <DeparturePicker value={departureAt} onChange={setDepartureAt} />;
  const targetSocEl = (
<label className="flex flex-col gap-1 text-sm">
              <span className="text-neutral-400">Target arrival SoC %</span>
              <input
                type="number"
                min={0}
                max={100}
                value={targetSoc}
                onChange={(e) => setTargetSoc(e.target.value)}
                className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
              />
            </label>
  );
  const driveModeEl = (
              <div role="radiogroup" aria-label="Drive mode" className="flex flex-col gap-1 text-sm">
                <span className="text-neutral-400">Drive mode</span>
                <div className="flex gap-1.5">
                  {([["EVERYDAY", "All-Purpose"], ["DISTANCE", "Conserve"]] as const).map(([v, l]) => (
                    <button
                      key={v}
                      type="button"
                      role="radio"
                      aria-checked={driveMode === v}
                      onClick={() => setDriveMode(v)}
                      className={`rounded-full border px-3 py-1.5 text-xs transition-colors ${driveMode === v ? "border-emerald-600/60 bg-emerald-600/15 text-emerald-200" : "border-neutral-700 text-neutral-300 hover:border-neutral-500"}`}
                    >
                      {l}
                    </button>
                  ))}
                </div>
              </div>
            );
  const adapterEl = vehicleNeedsTeslaAdapter(profileQuery.data, firstVehicle?.model_year) ? (

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
                    <span className="inline-block h-4 w-4 rounded-full bg-white shadow-sm" />
                  </span>
                </button>
              </div>
            
  ) : null;
  const fromEl = (
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
  );
  const viaEl = (
<ViaStopList
            stops={extraStops}
            overnightFlags={overnightFlags}
            onRemove={(i) => {
              setExtraStops((prev) => prev.filter((_, j) => j !== i));
              setOvernightFlags((prev) => prev.filter((_, j) => j !== i));
            }}
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
  );
  // Makes a place the night of a day: a new stop flagged as an overnight, or the existing stop at
  // that place. A listed charger's power (from a hotel pick) belongs to that night alone.
  const addOvernightStop = (stop: { lat: number; lon: number; label: string; l2Kw?: number; hookup?: HookupId; campground?: boolean }) => {
    const labeled = { ...relabelIfHome(stop, home) };
    const at = extraStops.findIndex(
      (s) => Math.abs(s.lat - labeled.lat) < 0.0005 && Math.abs(s.lon - labeled.lon) < 0.0005,
    );
    if (at >= 0) {
      // Already a stop on the trip: it becomes the night.
      setOvernightFlags((prev) => extraStops.map((_, j) => (j === at ? true : !!prev[j])));
    } else {
      setExtraStops((prev) => [...prev, labeled]);
      setOvernightFlags((prev) => [...extraStops.map((_, j) => !!prev[j]), true]);
    }
    // A place of your own starts as a J1772 station at 7.0 kW (you pick the hookup); a listed charger keeps its own power.
    const target = at >= 0 ? extraStops[at] : labeled;
    if (stop.hookup && stop.l2Kw !== undefined) updateOvernight(target, { l2Kw: stop.l2Kw, hookup: stop.hookup, campground: !!stop.campground }, false);
    else if (stop.l2Kw !== undefined) updateOvernight(target, { l2Kw: stop.l2Kw }, false);
    else updateOvernight(target, { l2Kw: DEFAULT_OVERNIGHT_KW, hookup: "j1772" }, false);
  };
  const overnightsEl = (
    <OvernightList
      stops={extraStops}
      overnightFlags={overnightFlags}
      optsFor={optsFor}
      onChange={updateOvernight}
      onRemove={(i) => {
        setExtraStops((prev) => prev.filter((_, j) => j !== i));
        setOvernightFlags((prev) => prev.filter((_, j) => j !== i));
      }}
      onAdd={addOvernightStop}
    />
  );
  const toEl = (
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
  );
  const planButtonEl = (
      <button
              type="submit"
              disabled={planMutation.isPending || multidayMutation.isPending || !origin || !destination}
              className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-800 disabled:cursor-not-allowed disabled:bg-neutral-800 disabled:text-neutral-500"
            >
              {hasOvernightStops ? `Plan ${overnightFlags.filter(Boolean).length + 1}-day trip` : "Plan trip"}
            </button>
  );
  const planSpinnerEl = (planMutation.isPending || multidayMutation.isPending) && <Spinner />;
  const planTipEl = (
      <span className="text-xs text-neutral-500">
              Tip: configure your charging settings in{" "}
              <a
                href={appPath("/settings?tab=charging")}
                className="underline hover:text-neutral-300"
              >
                Settings &rarr; Charging
              </a>{" "}
              to see accurate per-stop costs and membership savings.
            </span>
  );
  const moreCount = countNonDefaultOptions({
    viaStops: extraStops.filter((_, i) => !overnightFlags[i]).length,
    targetSoc,
    driveModeIsDefault: driveMode === "EVERYDAY",
    hasAdapter,
    avoidCount: [routePrefs.avoidTolls, routePrefs.avoidFerries, routePrefs.avoidHighways].filter(Boolean).length,
  });
  const formEl = (
    <>
      {inDemo() ? (
        <DemoPresetPicker activeId={activeTripId} onOpen={loadSavedTrip} />
      ) : (
      <FormShell hasResult={hasResult} resultKey={resultKey} summary={routeSummary}>
        {firstVehicle && (
          <p className="mb-3 text-xs text-neutral-500">
            Vehicle: <span className="font-mono">{firstVehicle.display_name || firstVehicle.rivian_vehicle_id}</span>
          </p>
        )}
        <form onSubmit={onSubmit} className="space-y-4" data-plan-form>
            <RouteBar
              from={fromEl}
              to={toEl}
              overnights={overnightsEl}
              onSwap={routeBar.swap}
              when={departureEl}
              soc={{
                value: startingSoc,
                live: typeof stateQuery.data?.battery_level_pct === "number" ? stateQuery.data.battery_level_pct : null,
                onChange: setStartingSoc,
              }}
              moreCount={moreCount}
              more={
                <>
                  {viaEl}
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-4" data-plan-settings>
                    {targetSocEl}
                    {driveModeEl}
                    {adapterEl}
                  </div>
                  <AvoidToggles value={routePrefs} onChange={setRoutePrefs} />
                  {planTipEl}
                </>
              }
              submit={
                <div className="flex items-center gap-3" data-plan-submit>
                  {planButtonEl}
                  {planSpinnerEl}
                </div>
              }
            />
        </form>
      </FormShell>
      )}
    </>
  );
  const pvaEl = (
    <>
      <PlanVsActualSection trips={savedTripsQuery.data ?? []} highlightId={activeTripId} />
    </>
  );
  // The trip being shown, named for a Save that is one tap on the result.
  const defaultTripName = (() => {
    const o = shortPlace(origin?.label);
    const d = shortPlace(destination?.label);
    const days = overnightFlags.filter(Boolean).length + 1;
    const base = o && d && o !== d ? `${o} to ${d}` : o ? `${o} round trip` : "My trip";
    return days > 1 ? `${base} · ${days} days` : base;
  })();
  const activeTripName = savedTripsQuery.data?.find((t) => t.id === activeTripId)?.name ?? null;
  const savedEl = (
    <>
      <SavedTripsCard
        trips={savedTripsQuery.data ?? []}
        loading={savedTripsQuery.isLoading}
        activeId={activeTripId}
        canSave={!!origin && !!destination}
        saveDisabled={saveTripMutation.isPending}
        suggestedName={origin && destination ? defaultTripName : undefined}
        onLoad={(t) => {
          loadSavedTrip(t);
          setPlanTab("planner");
        }}
        readOnly={inDemo()}
        onDelete={(id) => deleteTripMutation.mutate(id)}
        onSave={(name, id) => saveTripMutation.mutate({ name, id })}
        saveError={(saveTripMutation.error as Error | null)?.message}
      />
    </>
  );
  const saveBarEl =
    hasResult && !inDemo() ? (
      <SaveTripBar
        defaultName={defaultTripName}
        activeName={activeTripName}
        saving={saveTripMutation.isPending}
        error={saveTripMutation.error ? (saveTripMutation.error as Error).message : null}
        savedName={saveTripMutation.isSuccess ? saveTripMutation.data.name : null}
        onSave={(name) => saveTripMutation.mutate({ name, id: activeTripName === name ? activeTripId : null })}
        onShowSaved={() => setPlanTab("saved")}
      />
    ) : null;
  const resultEl = (
    <>
      {saveBarEl}
      {(planMutation.error || multidayMutation.isError) && (
        <PlanError error={planMutation.error ?? multidayMutation.error} />
      )}

      {multiday && (
        <div data-plan-wide data-plan-beside={inDemo() ? "" : undefined} className="space-y-3">
          {multidaySnapshot && activeTripId && !inDemo() && (
            <ReplanFromHere
              tripId={activeTripId}
              days={multidaySnapshot.response.days}
              base={replanBase}
              onPlanned={(merged, info) =>
                setMultidaySnapshot((prev) => (prev ? { ...prev, response: merged, replan: { fromDay: info.fromDay, position: info.position, mid: info.mid } } : prev))
              }
            />
          )}
          {multidaySnapshot && !multidaySnapshot.replan && !inDemo() && (
            <SnapshotBanner
              name={multidaySnapshot.name}
              updatedAt={multidaySnapshot.updatedAt}
              onReplan={() => {
                setMultidaySnapshot(null);
                multidayMutation.mutate();
              }}
              replanPending={multidayMutation.isPending}
            />
          )}
          <MultidayResult
            response={multiday}
            originLabel={origin?.label ?? ""}
            destLabel={destination?.label ?? ""}
            departureAt={departureAt}
            onAddOvernight={addOvernightStop}
            stays={Object.fromEntries(extraStops.filter((_, i) => overnightFlags[i]).map((st) => [st.label, { hookup: optsFor(st).hookup, kw: optsFor(st).l2Kw, campground: optsFor(st).campground }]))}
            replan={multidaySnapshot?.replan}
          />
        </div>
      )}

      {(() => {
        // Don't render the single-day card if the user just produced
        // a multi-day plan — the form's current state is multi-day.
        if (multiday) return null;
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
            {loadedSnapshot && !inDemo() && (
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
              aiEnabled={aiEnabled}
              aiOff={aiConsent.consent === false || isAIOff(adviceMutation.error)}
              adviceError={adviceMutation.error && !isAIOff(adviceMutation.error) ? (adviceMutation.error as Error).message : null}
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
                    ...(departureAt ? { depart_at: new Date(departureAt).toISOString() } : {}),
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
                    delta_cost: (comparableSpend(c1) ?? 0) - (comparableSpend(c0) ?? 0),
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
              onAddOvernight={addOvernightStop}
              departureAt={departureAt}
            />
          </ErrorBoundary>
        );
      })()}
    </>
  );

  const tabs: PlanTabSpec[] = [
    { id: "planner", label: "Planner" },
    { id: "saved", label: "Saved", count: savedTripsQuery.data?.length },
    { id: "pva", label: "Plan vs actual" },
  ];
  return (
    <div data-plan-next>
      <ConnectRivianPrompt context="The planner uses your truck's pack, current SoC, and adapter config — connect first for accurate routes." />
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold tracking-tight text-neutral-50">Trip planner</h1>
        <PlanTabs tabs={tabs} value={planTab} onChange={setPlanTab} />
      </div>
      <PlanTabPanel tab="planner" active={planTab === "planner"}>
        {/* One selected stop for the whole result: the itinerary, the map and
            anything that follows the selection. A new plan clears it. */}
        <PlanSelectionProvider routeKey={loadedSnapshot?.plan ?? planMutation.data ?? multiday ?? null}>
        <div data-plan-split data-has-result={hasResult ? "true" : undefined}>
          {formEl}
          {resultEl}
          {!hasResult && (
            <div data-plan-area="map" data-empty="true" aria-hidden>
              <div className="flex h-full items-center justify-center px-6 text-center text-sm text-neutral-500">
                {planMutation.isPending || multidayMutation.isPending ? "Planning your route…" : "Your route and charging stops appear here."}
              </div>
            </div>
          )}
        </div>
        </PlanSelectionProvider>
      </PlanTabPanel>
      <PlanTabPanel tab="saved" active={planTab === "saved"} className="space-y-6">
        {savedEl}
      </PlanTabPanel>
      <PlanTabPanel tab="pva" active={planTab === "pva"} className="space-y-6">
        {pvaEl}
      </PlanTabPanel>
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
  const stale = !inDemo() && ageH > SAVED_SNAPSHOT_STALE_HOURS;
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
        {inDemo() ? (
          <>
            Sample plan <strong className="text-neutral-100">"{name}"</strong>
          </>
        ) : (
          <>
            Showing saved snapshot <strong className="text-neutral-100">"{name}"</strong>{" "}
            from {human}
            {stale && " — charging stations, weather, and ETA may have drifted."}
          </>
        )}
      </span>
      {!inDemo() && (
        <button
          type="button"
          onClick={onReplan}
          disabled={replanPending}
          className="shrink-0 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-xs text-neutral-100 hover:border-neutral-500 disabled:opacity-50"
        >
          {replanPending ? "Re-planning…" : "Re-plan now"}
        </button>
      )}
    </div>
  );
}

// DemoPresetPicker: the public demo's planner. A handful of stored trips
// rendered by the real planner UI (route, stops, cost, arrival SoC); any
// other trip needs the visitor's own Rivian.
// FormShell: the classic card, or in ui_theme_next the compact route bar. On a
// phone the bar collapses to a one-line summary once there is a plan.
function FormShell({ hasResult, resultKey, summary, children }: { hasResult: boolean; resultKey: unknown; summary: string; children: React.ReactNode }) {
  const { collapsed, toggle, bodyId } = useRouteBarCollapse(hasResult, resultKey);
  return (
    <div data-plan-bar data-collapsed={collapsed ? "true" : undefined}>
      {hasResult && (
        <button
          type="button"
          data-plan-edit
          aria-expanded={!collapsed}
          aria-controls={bodyId}
          onClick={toggle}
          className="flex w-full items-center justify-between gap-3 text-left text-sm lg:hidden"
        >
          <span className="min-w-0 truncate text-neutral-100">{summary}</span>
          <span className="shrink-0 text-xs text-emerald-300">{collapsed ? "Edit" : "Done"}</span>
        </button>
      )}
      <div id={bodyId} data-plan-bar-body>{children}</div>
    </div>
  );
}

function DemoPresetPicker({ activeId, onOpen }: { activeId: string | null; onOpen: (t: SavedTrip) => void }) {
  const list = useQuery({ queryKey: ["demo", "trips"], queryFn: () => backend.demoTrips(), retry: 0, staleTime: 60 * 60_000 });
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const open = async (p: DemoTripPreset) => {
    setBusy(p.id);
    setErr(null);
    try {
      const fetched = await backend.demoTripPlan(p.id);
      // The preset's geometry rides on the route unless the plan carries its own. A multi-day preset is
      // the planner's multi-day answer ({days, total}), which opens like a saved multi-day plan.
      const geometry = p.route_polyline;
      const days = (fetched as Partial<SavedMultidayPlan>).days;
      const plan: TripPlan | SavedMultidayPlan = Array.isArray(days)
        ? { kind: "multiday", days, total: (fetched as SavedMultidayPlan).total, ...((fetched as SavedMultidayPlan).simulated ? { simulated: true } : {}) }
        : geometry
          ? { ...(fetched as TripPlan), Routes: (fetched as TripPlan).Routes.map((r, i) => (i === 0 && !r.route_polyline ? { ...r, route_polyline: geometry } : r)) }
          : (fetched as TripPlan);
      const now = new Date().toISOString();
      onOpen({ id: p.saved_trip_id ?? p.id, name: p.name, inputs: p.inputs, plan, created_at: now, updated_at: now });
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Couldn't load that trip.");
    } finally {
      setBusy(null);
    }
  };
  const trips = multiDayFirst(list.data?.trips ?? []);
  // /demo/trips/plan?preset=<id> (the sample-trip pages link here) opens that trip once the list is there.
  const presetParam = new URLSearchParams(useLocation().search).get("preset");
  const autoOpened = useRef(false);
  useEffect(() => {
    if (!presetParam || autoOpened.current || !list.data) return;
    const p = list.data.trips.find((x) => x.id === presetParam || x.saved_trip_id === presetParam);
    autoOpened.current = true;
    if (p) void open(p);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [presetParam, list.data]);
  return (
    <div data-plan-bar>
      <p className="ui-label mb-2">Sample trips</p>
      {list.isLoading ? (
        <Spinner />
      ) : (
        <ul className="flex flex-wrap gap-2" data-testid="demo-presets">
          {trips.map((p) => (
            <li key={p.id}>
              <button
                type="button"
                onClick={() => open(p)}
                disabled={busy != null}
                aria-pressed={p.id === activeId}
                className={`rounded-lg border px-3 py-2 text-left text-sm disabled:opacity-60 ${
                  p.id === activeId ? "border-emerald-600/60 bg-emerald-600/10" : "border-neutral-800 hover:border-neutral-600"
                }`}
              >
                <span className="flex flex-wrap items-center gap-x-2 text-neutral-100">
                  {p.name}
                  {tripTag(p.inputs) && (
                    <span className="rounded-sm bg-emerald-900/40 px-1.5 py-0.5 text-[11px] font-medium text-emerald-200">{tripTag(p.inputs)}</span>
                  )}
                </span>
                <span className="block text-xs text-neutral-500">
                  {tripSubtitle(p.inputs)} · {Math.round(p.summary.distance_mi)} mi
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {err && <p className="mt-2 text-xs text-rose-400">{err}</p>}
      <div className="mt-2">
        <ConnectHint what="plan any trip, with your truck's pack and charge level" />
      </div>
    </div>
  );
}

function SavedTripsCard({
  trips,
  loading,
  activeId,
  canSave,
  saveDisabled,
  suggestedName,
  onLoad,
  onDelete,
  onSave,
  saveError,
  readOnly = false,
}: {
  readOnly?: boolean;
  trips: SavedTrip[];
  loading: boolean;
  activeId: string | null;
  canSave: boolean;
  saveDisabled: boolean;
  // Filled in when there is no name yet, so Save is never a dead button with no hint why.
  suggestedName?: string;
  onLoad: (t: SavedTrip) => void;
  onDelete: (id: string) => void;
  onSave: (name: string, id: string | null) => void;
  saveError?: string;
}) {
  const [name, setName] = useState("");
  const active = trips.find((t) => t.id === activeId) ?? null;
  useEffect(() => {
    setName(active?.name ?? suggestedName ?? "");
  }, [active?.id, active?.name, suggestedName]);
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
    <Card title="Saved trips" area="p-saved">
      {loading ? (
        <Spinner />
      ) : trips.length === 0 ? (
        <p className="text-xs text-neutral-500">
          {readOnly
            ? "No sample trips here."
            : "No saved trips yet. Plan a route, then save it below so you can re-open or re-plan it later."}
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
                    {tripSubtitle(t.inputs)}
                  </span>
                </button>
                {!readOnly && (
                  <button
                    type="button"
                    onClick={() => {
                      if (confirm(`Delete saved trip "${t.name}"?`)) onDelete(t.id);
                    }}
                    className="shrink-0 text-xs text-neutral-500 hover:text-neutral-300"
                  >
                    delete
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {readOnly ? (
        <ConnectHint what="save your own trips" />
      ) : (
      <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={active ? `Update "${active.name}"` : "Save current trip as…"}
          className="flex-1 min-w-48 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-100 placeholder-neutral-600 focus:border-neutral-500 focus:outline-hidden"
        />
        <button
          type="submit"
          disabled={!canSave || saveDisabled || !name.trim()}
          className="rounded-md bg-neutral-800 px-3 py-1.5 text-sm text-neutral-100 hover:bg-neutral-700 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {active && active.name === name.trim() ? "Update" : "Save"}
        </button>
      </form>
      )}
      {!readOnly && !canSave && (
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
  // One compact chip row: Now, quick picks, then the exact date and time.
  return (
    <div data-plan-when-inner role="group" aria-label="Departure">
      <span className="ui-label">Departure</span>
      <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
        <button type="button" aria-pressed={!value} data-plan-chip onClick={() => onChange("")}>Now</button>
        {presets.map((p) => (
          <button key={p.label} type="button" data-plan-chip onClick={() => onChange(p.build())}>
            {p.label}
          </button>
        ))}
        <DatePopover value={date || ""} onChange={setDate} />
        <TimePopover value={time || ""} onChange={setTime} />
      </div>
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
        className="flex items-center gap-2 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 hover:border-neutral-500 focus:outline-hidden"
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
              className="rounded-sm px-2 py-1 text-neutral-400 hover:bg-neutral-800 hover:text-neutral-100"
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
              className="rounded-sm px-2 py-1 text-neutral-400 hover:bg-neutral-800 hover:text-neutral-100"
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
        className="flex items-center gap-2 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 hover:border-neutral-500 focus:outline-hidden"
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
      <div className="h-40 w-12 overflow-y-auto rounded-sm border border-neutral-800">
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
  // One compact row: a coloured node, the place (or the search), presets below.
  return (
    <div data-plan-field data-heading={heading} role="group" aria-label={heading}>
      <span data-plan-dot aria-hidden />
      <div data-plan-field-main>
        {value ? (
          <div className="flex items-center justify-between gap-2">
            <span className="pt-value min-w-0 truncate text-neutral-100">{value.label === "Home" ? "🏠 Home" : value.label}</span>
            <span className="flex shrink-0 items-center gap-3 text-xs">
              {onSaveAsFavorite && value.label !== "Home" && (
                <button type="button" onClick={() => onSaveAsFavorite(value)} className="text-amber-400 hover:text-amber-200" title="Save this place as a one-tap preset" aria-label={`Save place: ${value.label}, as a favorite`}>
                  ☆ save place
                </button>
              )}
              <button type="button" onClick={() => onChange(null)} aria-label={`Clear ${heading}`} className="text-neutral-400 hover:text-neutral-200">
                clear
              </button>
            </span>
          </div>
        ) : (
          <LocationSearch
            placeholder={`${heading} — city or address`}
            onSelect={(r) => onChange({ lat: r.latitude, lon: r.longitude, label: formatGeocode(r) })}
          />
        )}
        {!value && presets.length > 0 && (
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {presets.map((p) => (
              <button key={p.label} type="button" data-plan-chip data-home={p.label === "Home" ? "true" : undefined} onClick={() => onChange({ lat: p.lat, lon: p.lon, label: p.label })}>
                {p.label === "Home" ? "🏠 " : ""}{p.label}
              </button>
            ))}
          </div>
        )}
      </div>
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
          className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 pr-8 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
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

// PlanError is the inline failure under the form, scrolled into view because on
// a phone the result area starts below the fold.
function PlanError({ error }: { error: unknown }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [error]);
  const { title, detail } = planErrorCopy(error);
  return (
    <div ref={ref}>
      <ErrorBox title={title} detail={detail} />
    </div>
  );
}

// OvernightList is the nights of the trip, in the route bar so adding one needs no
// digging: each has its own stop, leave time, charger and charge cap.
function OvernightList({
  stops,
  overnightFlags,
  optsFor,
  onChange,
  onRemove,
  onAdd,
}: {
  stops: NonNullable<Selection>[];
  overnightFlags: boolean[];
  optsFor: (s: { lat: number; lon: number }) => OvernightOptions;
  onChange: (s: { lat: number; lon: number }, patch: Partial<OvernightOptions>) => void;
  onRemove: (i: number) => void;
  onAdd: StopAdder;
}) {
  const [adding, setAdding] = useState(false);
  const nights = stops.map((stop, i) => ({ stop, i })).filter((x) => overnightFlags[x.i]);
  const fieldCls = "rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-0.5 text-neutral-200 tabular-nums";
  return (
    <div data-plan-overnights className="space-y-2">
      {nights.length === 0 && !adding && (
        <p className="text-xs text-neutral-400">
          Going further than a day? Add an overnight and Rivolt plans each day, including charging while you sleep.
        </p>
      )}
      {nights.map(({ stop, i }, n) => {
        const o = optsFor(stop);
        return (
          <div key={i} data-overnight-stop className="space-y-2 rounded-lg border border-neutral-800 bg-neutral-950/40 px-3 py-2 text-sm">
            <div className="flex items-center gap-2">
              <span className="shrink-0 text-neutral-500">Night {n + 1}:</span>
              <span className="flex-1 text-neutral-100">{stop.label}</span>
              <button
                type="button"
                onClick={() => onRemove(i)}
                aria-label={`Remove night ${n + 1}`}
                className="shrink-0 text-xs text-neutral-500 hover:text-neutral-300"
              >
                remove
              </button>
            </div>
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-neutral-300">
              <label className="flex items-center gap-1.5">
                <span className="text-neutral-400">Leave next morning at</span>
                <input
                  type="time"
                  value={o.until}
                  onChange={(e) => onChange(stop, { until: e.target.value || "08:00" })}
                  className={fieldCls}
                />
              </label>
              <label className="flex w-full flex-wrap items-center gap-1.5 sm:w-auto">
                <span className="text-neutral-400">Hookup at the stop</span>
                <select
                  value={o.hookup ?? hookupForKw(o.l2Kw)}
                  onChange={(e) => {
                    const h = hookupById(e.target.value);
                    if (h) onChange(stop, { hookup: h.id, l2Kw: h.kw });
                  }}
                  className={`${fieldCls} w-full min-w-0 sm:w-auto sm:max-w-xs`}
                >
                  {HOOKUPS.map((h) => (
                    <option key={h.id} value={h.id}>{h.label}</option>
                  ))}
                </select>
              </label>
              {o.l2Kw > 0 && (
                <label className="flex items-center gap-1.5">
                  <span className="text-neutral-400">Planned power</span>
                  <input
                    type="number"
                    min={hookupById(o.hookup)?.min ?? KW_MIN}
                    max={hookupById(o.hookup)?.max ?? KW_MAX}
                    step={0.1}
                    value={o.l2Kw}
                    onChange={(e) => onChange(stop, { l2Kw: clampKw(Number(e.target.value), o.hookup) })}
                    aria-label="Planned power in kW"
                    className={`w-16 ${fieldCls}`}
                  />
                  <span className="text-neutral-400">kW</span>
                </label>
              )}
              <label className="flex items-center gap-1.5">
                <span className="text-neutral-400">Charge up to</span>
                <input
                  type="number"
                  min={50}
                  max={100}
                  step={5}
                  value={o.maxSocPct}
                  onChange={(e) => onChange(stop, { maxSocPct: Number(e.target.value) || 80 })}
                  className={`w-16 ${fieldCls}`}
                />
                <span className="text-neutral-400">%</span>
              </label>
            </div>
            <p data-hookup-note className="text-xs text-neutral-500">
              {hookupNote(o.hookup ?? hookupForKw(o.l2Kw), o.l2Kw)}{" "}
              {o.l2Kw > 0 ? `Charging is capped at ${o.maxSocPct}% for the hours you're parked.` : ""} Pick from the map to use a stay's listed charger.
            </p>
          </div>
        );
      })}
      {adding ? (
        <div className="rounded-lg border border-neutral-800 bg-neutral-950/40 px-3 py-2">
          <div className="mb-2 flex items-center justify-between">
            <span className="text-xs text-neutral-400">Where will you stop for the night?</span>
            <button type="button" onClick={() => setAdding(false)} className="text-xs text-neutral-500 hover:text-neutral-300">
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
          className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-200 hover:border-neutral-500"
        >
          + Add an overnight
        </button>
      )}
    </div>
  );
}

function ViaStopList({
  stops,
  overnightFlags,
  onRemove,
  onAdd,
}: {
  stops: NonNullable<Selection>[];
  overnightFlags: boolean[];
  onRemove: (i: number) => void;
  onAdd: StopAdder;
}) {
  const [adding, setAdding] = useState(false);
  return (
    <div className="space-y-2">
      {stops.map((stop, i) => overnightFlags[i] ? null : (
        <div
          key={i}
          className="flex flex-wrap items-center gap-2 rounded-lg border border-neutral-800 bg-neutral-950/40 px-3 py-2 text-sm"
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
