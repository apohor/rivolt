import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend, type VehicleProfile, type VehicleTireType } from "../lib/api";
import { ErrorBox, Spinner, Toggle } from "./ui";

// VehicleProfilePanel lets the user persist per-vehicle context the
// efficiency analyzer factors into its breakdown: tire type, wheel
// size, accessories, default extra load, frequently_tows. Stored in
// vehicles.metadata.profile via PUT /api/vehicles/{id}/profile.

const ACCESSORY_OPTIONS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "roof_rack", label: "Roof rack" },
  { value: "cargo_box", label: "Cargo box" },
  { value: "bike_rack", label: "Bike rack" },
  { value: "rooftop_tent", label: "Rooftop tent" },
  { value: "ski_box", label: "Ski / cargo box (winter)" },
  { value: "running_boards", label: "Running boards" },
];

const TIRE_OPTIONS: ReadonlyArray<{ value: VehicleTireType; label: string }> = [
  { value: "", label: "Unset" },
  { value: "all_season", label: "All-season" },
  { value: "all_terrain", label: "All-terrain" },
  { value: "winter", label: "Winter" },
  { value: "summer", label: "Summer" },
];

const WHEEL_OPTIONS = [0, 20, 21, 22] as const;

export function VehicleProfilePanel() {
  const vehiclesQ = useQuery({
    queryKey: ["vehicles", "owned"],
    queryFn: () => backend.listOwnedVehicles(),
  });
  const vehicles = useMemo(
    () => vehiclesQ.data?.vehicles ?? [],
    [vehiclesQ.data],
  );
  const [vehicleID, setVehicleID] = useState("");
  useEffect(() => {
    if (!vehicleID && vehicles.length > 0) {
      setVehicleID(vehicles[0].rivian_vehicle_id);
    }
  }, [vehicles, vehicleID]);

  if (vehiclesQ.isLoading) return <Spinner />;
  if (vehiclesQ.isError) {
    return (
      <ErrorBox
        title="Couldn't load vehicles"
        detail={String(vehiclesQ.error)}
      />
    );
  }
  if (vehicles.length === 0) {
    return (
      <p className="text-xs text-neutral-500">
        Sign in to your Rivian account first — the picker fills once
        Rivolt has seen at least one vehicle.
      </p>
    );
  }

  return (
    <div className="space-y-4 text-sm">
      {vehicles.length > 1 ? (
        <div>
          <label
            htmlFor="profile-vehicle"
            className="block text-xs text-neutral-400 mb-1"
          >
            Vehicle
          </label>
          <select
            id="profile-vehicle"
            value={vehicleID}
            onChange={(e) => setVehicleID(e.target.value)}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1.5 text-xs text-neutral-200 focus:border-emerald-500/60 focus:outline-hidden"
          >
            {vehicles.map((v) => (
              <option key={v.rivian_vehicle_id} value={v.rivian_vehicle_id}>
                {v.display_name || v.model || v.rivian_vehicle_id}
              </option>
            ))}
          </select>
        </div>
      ) : null}
      {vehicleID ? <ProfileForm key={vehicleID} vehicleID={vehicleID} /> : null}
    </div>
  );
}

function ProfileForm({ vehicleID }: { vehicleID: string }) {
  const profileQ = useQuery({
    queryKey: ["vehicle", vehicleID, "profile"],
    queryFn: () => backend.vehicleProfileGet(vehicleID),
  });

  const [tireType, setTireType] = useState<VehicleTireType>("");
  const [wheelInches, setWheelInches] = useState<number>(0);
  const [accessories, setAccessories] = useState<string[]>([]);
  const [extraLoadLb, setExtraLoadLb] = useState("");
  const [frequentlyTows, setFrequentlyTows] = useState(false);
  const [tirePlacardPsi, setTirePlacardPsi] = useState("");
  // "" = auto (model-year heuristic), "native" = NACS port, "ccs" = needs adapter.
  const [chargePort, setChargePort] = useState<"" | "native" | "ccs">("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<number | null>(null);

  // Hydrate form state from the GET once it lands.
  useEffect(() => {
    const p = profileQ.data;
    if (!p) return;
    setTireType((p.tire_type ?? "") as VehicleTireType);
    setWheelInches(p.wheel_inches ?? 0);
    setAccessories(p.accessories ?? []);
    setExtraLoadLb(
      p.default_extra_load_lb && p.default_extra_load_lb > 0
        ? String(p.default_extra_load_lb)
        : "",
    );
    setFrequentlyTows(!!p.frequently_tows);
    setTirePlacardPsi(
      p.tire_placard_psi && p.tire_placard_psi > 0
        ? String(p.tire_placard_psi)
        : "",
    );
    setChargePort(
      typeof p.native_nacs === "boolean"
        ? (p.native_nacs ? "native" : "ccs")
        : "",
    );
  }, [profileQ.data]);

  function toggleAccessory(value: string) {
    setAccessories((prev) =>
      prev.includes(value)
        ? prev.filter((v) => v !== value)
        : [...prev, value],
    );
  }

  async function save() {
    setBusy(true);
    setErr(null);
    try {
      const body: VehicleProfile = {
        tire_type: tireType || undefined,
        wheel_inches: wheelInches || undefined,
        accessories: accessories.length > 0 ? accessories : undefined,
        default_extra_load_lb:
          Number(extraLoadLb) > 0 ? Number(extraLoadLb) : undefined,
        frequently_tows: frequentlyTows || undefined,
        tire_placard_psi:
          Number(tirePlacardPsi) > 0 ? Number(tirePlacardPsi) : undefined,
        native_nacs:
          chargePort === "native" ? true : chargePort === "ccs" ? false : null,
      };
      await backend.vehicleProfilePut(vehicleID, body);
      setSavedAt(Date.now());
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  if (profileQ.isLoading) return <Spinner />;
  if (profileQ.isError) {
    return (
      <ErrorBox
        title="Couldn't load profile"
        detail={String(profileQ.error)}
      />
    );
  }

  return (
    <form
      className="space-y-3 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <p className="text-xs text-neutral-500">
        These settings tell the efficiency analyzer about your vehicle's
        rolling resistance, drag, and typical load. They affect every
        future analysis. Per-trip overrides (towing this trip, extra
        cargo) are entered on the drive detail page.
      </p>

      <div className="flex flex-wrap items-end gap-3">
        <div>
          <label
            htmlFor="profile-tire-type"
            className="block text-xs text-neutral-400 mb-1"
          >
            Tire type
          </label>
          <select
            id="profile-tire-type"
            value={tireType}
            onChange={(e) => setTireType(e.target.value as VehicleTireType)}
            className="rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200"
          >
            {TIRE_OPTIONS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label
            htmlFor="profile-wheel"
            className="block text-xs text-neutral-400 mb-1"
          >
            Wheel size
          </label>
          <select
            id="profile-wheel"
            value={wheelInches}
            onChange={(e) => setWheelInches(Number(e.target.value))}
            className="rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200"
          >
            {WHEEL_OPTIONS.map((n) => (
              <option key={n} value={n}>
                {n === 0 ? "Unset" : `${n}"`}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label
            htmlFor="profile-extra-load"
            className="block text-xs text-neutral-400 mb-1"
          >
            Default extra load
          </label>
          <div className="flex items-center gap-1">
            <input
              id="profile-extra-load"
              type="number"
              inputMode="numeric"
              min={0}
              max={5000}
              step={10}
              placeholder="0"
              value={extraLoadLb}
              onChange={(e) => setExtraLoadLb(e.target.value)}
              className="w-24 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
            />
            <span className="text-xs text-neutral-500">lb</span>
          </div>
        </div>
        <div>
          <label
            htmlFor="profile-tire-placard"
            className="block text-xs text-neutral-400 mb-1"
            title={'Cold-fill pressure listed on your driver-door jamb sticker. R1S 22" road is 42 psi front / 44 psi rear; check yours.'}
          >
            Tire placard PSI
          </label>
          <div className="flex items-center gap-1">
            <input
              id="profile-tire-placard"
              type="number"
              inputMode="numeric"
              min={0}
              max={80}
              step={1}
              placeholder="42"
              value={tirePlacardPsi}
              onChange={(e) => setTirePlacardPsi(e.target.value)}
              className="w-20 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
            />
            <span className="text-xs text-neutral-500">psi</span>
          </div>
        </div>
        <div>
          <label
            htmlFor="profile-charge-port"
            className="block text-xs text-neutral-400 mb-1"
            title="Auto uses model_year ≥ 2026 → native NACS as a default."
          >
            Charge port
          </label>
          <select
            id="profile-charge-port"
            value={chargePort}
            onChange={(e) =>
              setChargePort(e.target.value as "" | "native" | "ccs")
            }
            className="rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200"
          >
            <option value="">Auto (by model year)</option>
            <option value="native">Native NACS</option>
            <option value="ccs">CCS (needs Tesla adapter)</option>
          </select>
        </div>
      </div>
      <p className="text-[11px] text-neutral-500 -mt-1">
        Tire placard PSI is the cold-fill pressure on the driver-door
        jamb sticker. The efficiency analyzer uses this to compute
        underinflation against ground truth instead of guessing the
        placard from generic priors. Leave 0 to skip.
      </p>
      <p className="text-[11px] text-neutral-500">
        Charge port controls whether the planner shows the "Tesla NACS
        adapter" toggle. Auto defaults to native on MY2026+ R1.
      </p>

      <div>
        <label
          htmlFor="profile-frequently-tows"
          className="flex items-center gap-2 text-neutral-300"
        >
          <Toggle
            id="profile-frequently-tows"
            checked={frequentlyTows}
            onChange={setFrequentlyTows}
          />
          Frequently tows
        </label>
      </div>

      <div>
        <div className="text-xs text-neutral-400 mb-1">Accessories</div>
        <div className="grid grid-cols-2 gap-y-1 sm:grid-cols-3">
          {ACCESSORY_OPTIONS.map((a) => (
            <label
              key={a.value}
              className="flex items-center gap-2 text-neutral-300"
            >
              <input
                type="checkbox"
                checked={accessories.includes(a.value)}
                onChange={() => toggleAccessory(a.value)}
                className="h-3.5 w-3.5 accent-emerald-500"
              />
              {a.label}
            </label>
          ))}
        </div>
      </div>

      <div className="flex items-center gap-3 pt-1">
        <button
          type="submit"
          disabled={busy}
          className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
        >
          {busy ? "Saving…" : "Save"}
        </button>
        {savedAt ? (
          <span className="text-xs text-neutral-500">Saved.</span>
        ) : null}
      </div>

      {err ? <ErrorBox title="Save failed" detail={err} /> : null}
    </form>
  );
}
