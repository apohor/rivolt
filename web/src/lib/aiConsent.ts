import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, backend } from "./api";
import { inDemo } from "./demo";

// The user's own AI switch. Off until they turn it on in Settings; the AI calls refuse (403, class ai_off) before then.
// `consent` is undefined while it loads. A server from before the switch (404) has no switch to turn on, so its calls are
// tap-only and the answer is read as on; a response with no `enabled` in it reads as off.
export function useAIConsent() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["ai-consent"], queryFn: () => backend.aiConsentGet(), staleTime: 60_000, retry: 0, enabled: !inDemo() });
  const set = useMutation({
    mutationFn: (enabled: boolean) => backend.aiConsentPut(enabled),
    onSuccess: (d) => qc.setQueryData(["ai-consent"], d),
  });
  // The demo has no AI and no account: nothing to ask, nothing to show.
  const consent: boolean | undefined = inDemo() ? true : q.isPending ? undefined : q.isError ? false : (q.data === null ? true : (q.data?.enabled ?? false));
  return { consent, set: set.mutateAsync, saving: set.isPending, error: set.error as Error | null };
}

// A 403 from an AI call that means "not turned on", as opposed to any other failure.
export function isAIOff(err: unknown): boolean {
  return err instanceof ApiError && err.status === 403 && (err.body as { class?: string } | null)?.class === "ai_off";
}

export const AI_SETTINGS_PATH = "/settings?tab=account#ai-features";
