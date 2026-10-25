import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AccountUnits, type AuthUser, type UnitsPatch } from "./api";
import { inDemo } from "./demo";
import { applyAccountUnits, hasLocalUnitPick, localUnits, onUnitPick, type DistanceUnit, type TemperatureUnit } from "./preferences";

// Distance and temperature units live on the account, so the web and the iPhone app agree. This browser keeps a copy (so the
// signed-out pages and the demo work) and follows the account when signed in.

// The distance unit a region uses: miles in the US, Liberia, Myanmar and the UK, kilometres elsewhere; null when the browser
// names no region (nothing is guessed, and the IP address is never used).
export function regionDistanceUnit(language: string | null | undefined): DistanceUnit | null {
  if (!language) return null;
  let region: string | undefined;
  try {
    region = new Intl.Locale(language).maximize().region;
  } catch {
    return null;
  }
  if (!region) return null;
  return ["US", "LR", "MM", "GB"].includes(region) ? "mi" : "km";
}

export type Local = { distanceUnit: DistanceUnit; temperatureUnit: TemperatureUnit; temperatureUnitSet: boolean; picked: boolean };
export type Plan = {
  // Take the account's units into this browser.
  apply?: { distance: DistanceUnit; temperature: TemperatureUnit; temperatureSet: boolean };
  // Send this browser's choice up (the account has none yet).
  patch?: UnitsPatch;
};

// What to do with the account's units and this browser's saved ones. An account with a value wins and the local copy is dropped;
// an account with none takes this browser's choice once, or else is seeded once from the region.
export function reconcile(account: AccountUnits, local: Local, region: DistanceUnit | null): Plan {
  if (account.distance_source !== "unset") {
    return { apply: { distance: account.distance_unit, temperature: account.temperature_unit, temperatureSet: account.temperature_source === "user" } };
  }
  if (local.picked) {
    return { patch: { distance_unit: local.distanceUnit, ...(local.temperatureUnitSet ? { temperature_unit: local.temperatureUnit } : {}) } };
  }
  if (region) return { patch: { seed: true, distance_unit: region }, apply: { distance: region, temperature: region === "km" ? "c" : "f", temperatureSet: false } };
  return {};
}

// Mounted once for a signed-in user (not in the demo): reconciles at the start, then sends each pick made in Settings up.
export function useAccountUnits(me: AuthUser | null | undefined) {
  const qc = useQueryClient();
  const userID = me?.user_id;
  const enabled = !!userID && !inDemo();
  // A server that sends the units in /api/auth/me needs no second request.
  const fetched = useQuery({ queryKey: ["settings", "units"], queryFn: () => backend.getUnits(), enabled: enabled && !me?.units, retry: false, staleTime: Infinity });
  const units = me?.units ?? fetched.data;
  const distanceSource = units?.distance_source;

  useEffect(() => {
    if (!enabled || !units) return;
    const plan = reconcile(units, { ...localUnits(), picked: hasLocalUnitPick() }, regionDistanceUnit(typeof navigator === "undefined" ? undefined : navigator.language));
    if (plan.apply) applyAccountUnits(plan.apply.distance, plan.apply.temperature, plan.apply.temperatureSet);
    if (plan.patch) void backend.patchUnits(plan.patch).then((u) => qc.setQueryData(["settings", "units"], u)).catch(() => {});
    // Only when the account's answer changes (a new user, or its source after a save), not on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, userID, distanceSource, units?.distance_unit, units?.temperature_unit, units?.temperature_source]);

  useEffect(() => {
    if (!enabled) return;
    return onUnitPick((p) => {
      void backend.patchUnits(p).then((u) => qc.setQueryData(["settings", "units"], u)).catch(() => {});
    });
  }, [enabled, qc]);
}
