import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { scopeSentence, scopeVehicleName, useVehicleScope } from "../lib/vehicleScope";

// "Showing R1T. Pro includes all vehicles." under a page that the server narrowed to a free account's primary vehicle. Nothing when the
// answer was not narrowed (the header is absent) or the vehicle is not in the list.
export function VehicleScopeNote() {
  const id = useVehicleScope();
  const vehicles = useQuery({ queryKey: ["vehicles"], queryFn: () => backend.vehicles(), enabled: !!id });
  const name = id && vehicles.data ? scopeVehicleName(vehicles.data, id) : null;
  if (!name) return null;
  return (
    <p data-vehicle-scope role="note" className="text-xs text-neutral-400">
      {scopeSentence(name)}
    </p>
  );
}
