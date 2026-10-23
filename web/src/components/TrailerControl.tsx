import { useMutation, useQueryClient } from "@tanstack/react-query";
import { backend, type Drive } from "../lib/api";
import { Card } from "./ui";

// What the user can say about a drive's trailer: nothing (use what the truck reported), yes, or no.
type Choice = "auto" | "yes" | "no";

export function choiceOf(override: boolean | null | undefined): Choice {
  return override == null ? "auto" : override ? "yes" : "no";
}
export function overrideOf(c: Choice): boolean | null {
  return c === "auto" ? null : c === "yes";
}
function recordedLabel(t: boolean | null | undefined): string {
  return t == null ? "the truck did not say" : t ? "the truck reported a trailer" : "the truck reported no trailer";
}

// Lets the user correct whether a trailer was connected on a drive. A drive with a trailer is left out of the efficiency
// figures, so a wrong guess either way skews them. The signal is the connector, not the load.
export function TrailerControl({ drive }: { drive: Pick<Drive, "ID" | "Trailer" | "TrailerOverride"> }) {
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: (c: Choice) => backend.driveTrailer(drive.ID, overrideOf(c)),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["drives"] });
      void qc.invalidateQueries({ queryKey: ["insights"] });
    },
  });
  const value = m.isPending && m.variables ? m.variables : choiceOf(drive.TrailerOverride);
  return (
    <Card>
      <label className="flex flex-wrap items-center justify-between gap-3 text-sm" data-trailer-control>
        <span>Trailer</span>
        <select
          value={value}
          disabled={m.isPending}
          onChange={(e) => m.mutate(e.target.value as Choice)}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm"
        >
          <option value="auto">Automatic ({recordedLabel(drive.Trailer)})</option>
          <option value="yes">Trailer connected</option>
          <option value="no">No trailer</option>
        </select>
      </label>
      <p className="mt-2 text-xs text-neutral-500">
        A drive with a trailer is left out of the efficiency figures. The truck reports a trailer connector, not what is on it, so a bike rack
        or cargo carrier with lights can read as a trailer.
      </p>
      {m.isError && <p className="mt-2 text-xs text-red-400">Could not save that. Try again.</p>}
    </Card>
  );
}
