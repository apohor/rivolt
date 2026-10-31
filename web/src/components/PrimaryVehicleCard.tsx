import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type Vehicle } from "../lib/api";
import { planState } from "../lib/plan";
import { Card } from "./ui";

// Settings → Vehicle → "Vehicle the free plan shows": a free account with several vehicles shows one (its primary); the others keep
// recording and alerting and stay in the export, and Pro shows them all. Only where the server bills, only for an account on the free
// plan with more than one vehicle. Choosing is free and changes nothing that is stored.
export function PrimaryVehicleCard() {
  const qc = useQueryClient();
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const vehicles = useQuery({ queryKey: ["vehicles"], queryFn: () => backend.vehicles(), staleTime: 5 * 60_000 });
  const plan = planState(me.data);
  const limited = plan.kind === "free" || plan.kind === "lapsed";
  const list = vehicles.data ?? [];
  const shownID = list.find((v) => !v.pro_locked)?.id ?? "";
  const [pick, setPick] = useState(shownID);
  useEffect(() => setPick(shownID), [shownID]);
  const save = useMutation({
    mutationFn: (id: string) => backend.setPrimaryVehicle(id),
    // The vehicle list carries each vehicle's lock: move it locally (the server's answer is the same), so nothing else refetches.
    onSuccess: (_, id) => qc.setQueryData<Vehicle[]>(["vehicles"], (old) => old?.map((v) => ({ ...v, pro_locked: v.id !== id }))),
  });
  if (!limited || list.length < 2) return null;
  const label = (v: (typeof list)[number]) => (v.name && v.name.trim() ? v.name : [v.model, v.vin?.slice(-6)].filter(Boolean).join(" · ") || v.id.slice(0, 8));
  return (
    <Card title="Vehicle the free plan shows" id="primary-vehicle">
      <div data-primary-vehicle className="space-y-3 text-sm">
        <p className="text-neutral-300">
          The free plan shows one vehicle. The others keep recording and alerting, stay in your export, and Pro shows them all. Nothing is deleted.
        </p>
        <fieldset className="space-y-2">
          <legend className="sr-only">Vehicle the free plan shows</legend>
          {list.map((v) => (
            <label key={v.id} className="flex items-center gap-2 text-neutral-200">
              <input type="radio" name="primary-vehicle" checked={pick === v.id} onChange={() => setPick(v.id)} />
              <span>{label(v)}</span>
            </label>
          ))}
        </fieldset>
        <button
          type="button"
          className="rounded-md border border-neutral-700 px-3 py-1.5 text-neutral-200 hover:border-neutral-500 disabled:opacity-50"
          disabled={!pick || pick === shownID || save.isPending}
          onClick={() => save.mutate(pick)}
        >
          Show this vehicle
        </button>
        {save.isError && <p role="alert" className="text-rose-400">Couldn&apos;t save that choice.</p>}
        {save.isSuccess && <p role="status" className="text-emerald-300">Done. The free plan now shows this vehicle.</p>}
      </div>
    </Card>
  );
}
