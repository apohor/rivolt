import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { summaryParts } from "../lib/truckSummary";

// TruckSummary: the line at the top of Truck, built from the Pack capacity and Parked drain
// cards' own data (the same queries, so no extra request). Each half is a 44 px target that
// scrolls to its card; nothing at all when neither card has a figure.
export function TruckSummary({ vehicleID }: { vehicleID: string }) {
  const pack = useQuery({
    queryKey: ["packHealth", vehicleID],
    queryFn: () => backend.packHealth(vehicleID),
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
  });
  const drain = useQuery({
    queryKey: ["vehicle", "drain", vehicleID],
    queryFn: () => backend.vehicleDrain(vehicleID),
    staleTime: 5 * 60_000,
    retry: 0,
  });
  const parts = summaryParts(pack.data, drain.data);
  if (parts.length === 0) return null;
  const go = (id: string) => document.getElementById(`truck-${id}`)?.scrollIntoView({ behavior: "smooth", block: "start" });
  const button = (p: (typeof parts)[number], text: string) => (
    <button
      type="button"
      onClick={() => go(p.id)}
      title={p.id === "pack" ? "Shows Pack capacity" : "Shows Parked drain"}
      className="min-h-11 text-left underline-offset-4 hover:underline"
    >
      {text}
    </button>
  );
  // A phone: one half per row, each a sentence (a wrapped line would start with "·"). Wider: one line.
  return (
    <section aria-label="Truck summary" data-truck-summary data-span="full" className="text-sm text-neutral-200">
      <div className="flex flex-col sm:hidden">
        {parts.map((p) => (
          <span key={p.id}>{button(p, p.text)}</span>
        ))}
      </div>
      <div className="hidden flex-wrap items-center sm:flex">
        {parts.map((p, i) => (
          <span key={p.id} className="inline-flex items-center">
            {i > 0 && (
              <span aria-hidden className="px-1.5 text-neutral-500">
                ·
              </span>
            )}
            {button(p, i === 0 ? p.text : p.text.charAt(0).toLowerCase() + p.text.slice(1))}
          </span>
        ))}
      </div>
    </section>
  );
}
