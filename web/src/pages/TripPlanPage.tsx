import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { backend, type TripPlan, type TripRoute } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";

// Slice 1 of the trip planner: a thin pass-through to Rivian's
// planTripWithMultiStop. The form takes raw lat/lon coords (geocoding
// lands in slice 2) plus an optional target arrival SoC. The server
// fills in vehicle_id and starting_soc from the live state cache,
// so the form stays minimal: where am I going, and how charged do I
// want to be when I get there?
//
// What this surfaces: per-stop arrival/departure SoC, charge duration,
// max power, adapter-required flag, total trip charging time, and any
// risk signals (socBelowLimit, batteryEmptyToDestinationDistance).
// No AI commentary, no map polyline, no save — those are slices 2+.
export default function TripPlanPage() {
  const [originLat, setOriginLat] = useState("");
  const [originLon, setOriginLon] = useState("");
  const [destLat, setDestLat] = useState("");
  const [destLon, setDestLon] = useState("");
  const [targetSoc, setTargetSoc] = useState<string>("20");

  const planMutation = useMutation({
    mutationFn: () => {
      const target = targetSoc.trim() === "" ? undefined : Number(targetSoc);
      return backend.planTrip({
        origin_bearing: 0,
        target_arrival_soc_percent: target,
        waypoints: [
          {
            latitude: Number(originLat),
            longitude: Number(originLon),
            waypoint_type: "origin",
          },
          {
            latitude: Number(destLat),
            longitude: Number(destLon),
            waypoint_type: "destination",
          },
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
        subtitle="Slice 1 — Rivian planTripWithMultiStop pass-through. Coordinates only; geocoding + AI commentary land later."
      />

      <Card title="Plan a trip">
        <form onSubmit={onSubmit} className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <CoordField label="Origin lat" value={originLat} setValue={setOriginLat} placeholder="30.5538" />
          <CoordField label="Origin lon" value={originLon} setValue={setOriginLon} placeholder="-97.7622" />
          <CoordField label="Destination lat" value={destLat} setValue={setDestLat} placeholder="32.7767" />
          <CoordField label="Destination lon" value={destLon} setValue={setDestLon} placeholder="-96.7970" />
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
          <div className="sm:col-span-2 flex items-center gap-3">
            <button
              type="submit"
              disabled={planMutation.isPending || !canSubmit({ originLat, originLon, destLat, destLon })}
              className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-medium text-emerald-50 hover:bg-emerald-600 disabled:cursor-not-allowed disabled:bg-neutral-800 disabled:text-neutral-500"
            >
              Plan trip
            </button>
            {planMutation.isPending && <Spinner />}
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

function CoordField({
  label,
  value,
  setValue,
  placeholder,
}: {
  label: string;
  value: string;
  setValue: (v: string) => void;
  placeholder: string;
}) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      <span className="text-neutral-400">{label}</span>
      <input
        type="text"
        inputMode="decimal"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={placeholder}
        className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 font-mono text-neutral-100 focus:border-neutral-500 focus:outline-none"
      />
    </label>
  );
}

function canSubmit({
  originLat,
  originLon,
  destLat,
  destLon,
}: {
  originLat: string;
  originLon: string;
  destLat: string;
  destLon: string;
}) {
  const fields = [originLat, originLon, destLat, destLon];
  return fields.every((s) => {
    const n = Number(s);
    return s.trim() !== "" && Number.isFinite(n);
  });
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
    (w) => w.WaypointType !== "origin" && w.WaypointType !== "destination",
  );
  const totalChargeMin = Math.round(route.TotalChargingDurationSec / 60);
  return (
    <Card title={`Route ${index + 1}${route.DestinationReached ? "" : " — destination unreachable"}`}>
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
