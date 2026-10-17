import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type LeaseTerms } from "../lib/api";
import { ErrorBox, Spinner } from "./ui";
import { validateLease, type LeaseErrors } from "../lib/lease";
import { usePreferences } from "../lib/preferences";

// Settings → Vehicle → Lease (new look): the terms the Truck lease card needs. Entered in the
// user's distance unit, stored in miles. Removing the lease hides the Truck card.
const KM = 1.609344;
type Draft = Record<keyof LeaseTerms, string>;
const EMPTY: Draft = { start_date: "", end_date: "", start_odometer_mi: "", allowed_miles_total: "", overage_per_mile: "", currency: "USD" };

export function LeasePanel() {
  const { distanceUnit: unit } = usePreferences();
  const qc = useQueryClient();
  const vehiclesQ = useQuery({ queryKey: ["vehicles", "owned"], queryFn: () => backend.listOwnedVehicles() });
  const vehicles = useMemo(() => vehiclesQ.data?.vehicles ?? [], [vehiclesQ.data]);
  const [vehicleID, setVehicleID] = useState("");
  useEffect(() => {
    if (!vehicleID && vehicles.length > 0) setVehicleID(vehicles[0].rivian_vehicle_id);
  }, [vehicles, vehicleID]);

  const leaseQ = useQuery({ queryKey: ["lease", "terms", vehicleID], queryFn: () => backend.leaseGet(vehicleID), enabled: !!vehicleID, retry: 0 });
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [errors, setErrors] = useState<LeaseErrors>({});
  const [saved, setSaved] = useState(false);
  const toUnit = (mi: number) => String(Math.round((unit === "km" ? mi * KM : mi) * 100) / 100);
  const toMi = (v: string) => (unit === "km" ? Number(v) / KM : Number(v));
  // Per-mile rate shown per-km for km users.
  const rateToUnit = (perMi: number) => String(Math.round((unit === "km" ? perMi / KM : perMi) * 10000) / 10000);
  const rateToMi = (v: string) => (unit === "km" ? Number(v) * KM : Number(v));

  useEffect(() => {
    const t = leaseQ.data;
    setSaved(false);
    setErrors({});
    setDraft(
      t
        ? { start_date: t.start_date, end_date: t.end_date, start_odometer_mi: toUnit(t.start_odometer_mi), allowed_miles_total: toUnit(t.allowed_miles_total), overage_per_mile: rateToUnit(t.overage_per_mile), currency: t.currency }
        : EMPTY,
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [leaseQ.data, vehicleID, unit]);

  const save = useMutation({
    mutationFn: () => {
      const terms: LeaseTerms = {
        start_date: draft.start_date,
        end_date: draft.end_date,
        start_odometer_mi: toMi(draft.start_odometer_mi),
        allowed_miles_total: toMi(draft.allowed_miles_total),
        overage_per_mile: rateToMi(draft.overage_per_mile),
        currency: draft.currency || "USD",
      };
      return backend.leasePut(vehicleID, terms);
    },
    onSuccess: () => {
      setSaved(true);
      void qc.invalidateQueries({ queryKey: ["lease"] });
    },
  });
  const remove = useMutation({
    mutationFn: () => backend.leaseDelete(vehicleID),
    onSuccess: () => {
      setSaved(false);
      void qc.invalidateQueries({ queryKey: ["lease"] });
    },
  });

  if (vehiclesQ.isLoading) return <Spinner />;
  if (vehiclesQ.isError) return <ErrorBox title="Couldn't load vehicles" detail={String(vehiclesQ.error)} />;
  if (vehicles.length === 0) return <p className="text-xs text-neutral-500">Connect your Rivian first; the lease is set per vehicle.</p>;

  const set = (k: keyof LeaseTerms) => (e: React.ChangeEvent<HTMLInputElement>) => {
    setSaved(false);
    setDraft((d) => ({ ...d, [k]: e.target.value }));
  };
  const field = (k: keyof LeaseTerms, label: string, props: React.InputHTMLAttributes<HTMLInputElement>, hint?: string) => (
    <label className="flex flex-col gap-1 text-sm">
      <span className="text-xs font-medium text-neutral-400">{label}</span>
      <input
        value={draft[k]}
        onChange={set(k)}
        aria-invalid={errors[k] ? true : undefined}
        aria-describedby={errors[k] ? `lease-${k}-err` : undefined}
        className="input"
        {...props}
      />
      {errors[k] ? <span id={`lease-${k}-err`} role="alert" className="text-xs text-rose-300">{errors[k]}</span> : hint ? <span className="text-xs text-neutral-500">{hint}</span> : null}
    </label>
  );
  const hasLease = !!leaseQ.data;
  return (
    <form
      data-lease-form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        const errs = validateLease({ ...draft });
        setErrors(errs);
        if (Object.keys(errs).length === 0) save.mutate();
      }}
    >
      {vehicles.length > 1 && (
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-xs font-medium text-neutral-400">Vehicle</span>
          <select value={vehicleID} onChange={(e) => setVehicleID(e.target.value)} className="input">
            {vehicles.map((v) => (
              <option key={v.rivian_vehicle_id} value={v.rivian_vehicle_id}>{v.display_name || v.model || v.rivian_vehicle_id}</option>
            ))}
          </select>
        </label>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {field("start_date", "Lease start", { type: "date" })}
        {field("end_date", "Lease end", { type: "date" })}
        {field("allowed_miles_total", `Miles allowed over the whole lease (${unit})`, { type: "number", inputMode: "decimal", min: 0, step: "any", placeholder: unit === "km" ? "57936" : "36000" })}
        {field("start_odometer_mi", `Odometer at lease start (${unit})`, { type: "number", inputMode: "decimal", min: 0, step: "any", placeholder: "12" })}
        {field("overage_per_mile", `Overage per ${unit}`, { type: "number", inputMode: "decimal", min: 0, step: "any", placeholder: "0.25" }, "What the lessor charges for each mile over; 0 if none.")}
        {field("currency", "Currency", { type: "text", maxLength: 3, placeholder: "USD", autoCapitalize: "characters" })}
      </div>
      {leaseQ.isError && <ErrorBox title="Couldn't load the lease" detail={String(leaseQ.error)} />}
      {(save.isError || remove.isError) && <ErrorBox title="Couldn't save" detail={String(save.error ?? remove.error)} />}
      <div className="flex flex-wrap items-center gap-3">
        <button
          type="submit"
          disabled={save.isPending || leaseQ.isLoading}
          className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-medium text-emerald-50 hover:bg-emerald-800 disabled:cursor-not-allowed disabled:bg-neutral-800 disabled:text-neutral-500"
        >
          {hasLease ? "Save lease" : "Add lease"}
        </button>
        {hasLease && (
          <button type="button" onClick={() => remove.mutate()} disabled={remove.isPending} className="text-sm text-neutral-400 underline-offset-2 hover:text-neutral-200 hover:underline">
            Remove lease
          </button>
        )}
        {saved && <span role="status" data-lease-saved className="text-sm text-emerald-300">Saved. See it on Truck.</span>}
      </div>
    </form>
  );
}
