import type { Sample } from "./api";

// GET /api/samples is scoped to the user, not the vehicle. For an
// account with two trucks (or a replay truck beside a real one) the
// window holds both tracks interleaved by time, which draws as a fan on
// the route map and a saw-tooth on the charts. Detail pages keep only
// their own vehicle's rows. When nothing matches (legacy rows whose
// vehicle id doesn't line up) the input is returned unchanged rather
// than blanking the page.
export function forVehicle(samples: Sample[], vehicleID: string | null | undefined): Sample[] {
  if (!vehicleID) return samples;
  const own = samples.filter((s) => s.VehicleID === vehicleID);
  return own.length > 0 ? own : samples;
}
