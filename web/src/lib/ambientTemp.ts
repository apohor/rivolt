import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend } from "./api";
import { inDemo } from "./demo";

// The text shown wherever the switch is offered (DA-approved wording).
export const AMBIENT_TEMP_TEXT =
  "Outside temperature: Rivian doesn't report it, so Rivolt looks it up from Open-Meteo using your truck's location rounded to about 11 km, at most every 30 minutes and only while you have Rivolt open. Off by default.";

// The outside-temperature switch. `enabled` is undefined while it loads and null where the server has no such
// setting; the demo has no account, so it never asks and never offers it.
export function useAmbientTemp() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["ambient-temp"], queryFn: () => backend.ambientTempGet(), staleTime: 60_000, retry: 0, enabled: !inDemo() });
  const set = useMutation({
    mutationFn: (enabled: boolean) => backend.ambientTempPut(enabled),
    onSuccess: (d) => {
      qc.setQueryData(["ambient-temp"], d);
      // The reading rides on /now; refetch it so it shows (or goes) right away.
      void qc.invalidateQueries({ queryKey: ["vehicle", "now"] });
    },
  });
  const enabled: boolean | null | undefined = inDemo() ? null : q.isPending ? undefined : q.isError ? null : q.data === null ? null : (q.data?.enabled ?? false);
  return { enabled, set: set.mutateAsync, saving: set.isPending, error: set.error as Error | null };
}
