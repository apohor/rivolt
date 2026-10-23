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
  // One copy of the line. A phone: one half per row, each a sentence (a wrapped line would
  // start with "·"), so the separator hides and the second half's first letter is a capital.
  // Wider: one line.
  return (
    <section
      aria-label="Truck summary"
      data-truck-summary
      data-span="full"
      className="flex flex-col text-sm text-neutral-200 sm:flex-row sm:flex-wrap sm:items-center"
    >
      {parts.map((p, i) => (
        <span key={p.id} className="inline-flex items-center">
          {i > 0 && (
            <span aria-hidden className="hidden px-1.5 text-neutral-500 sm:inline">
              ·
            </span>
          )}
          <button
            type="button"
            onClick={() => go(p.id)}
            title={p.id === "pack" ? "Shows Pack capacity" : "Shows Parked drain"}
            className={"min-h-11 text-left underline-offset-4 hover:underline" + (i > 0 ? " max-sm:first-letter:uppercase" : "")}
          >
            {i === 0 ? p.text : p.text.charAt(0).toLowerCase() + p.text.slice(1)}
          </button>
        </span>
      ))}
    </section>
  );
}
