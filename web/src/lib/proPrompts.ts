import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { backend, type AuthUser } from "./api";

// A Pro prompt that can be turned away with "Not now" (design key pro_entry; the store is the server's, per account). The account's list of
// hidden prompts comes with /api/auth/me. `me` is the caller's own /api/auth/me read. `dismiss` hides the prompt at once and tells the server; when the server refuses, the list is read again.
export type ProPromptWhere = "planner" | "lapse_banner";

export function useProPrompt(where: ProPromptWhere, me: AuthUser | null | undefined) {
  const qc = useQueryClient();
  const hidden = !!me?.pro_prompts_hidden?.includes(where);
  const mark = useCallback(() => {
    qc.setQueryData<AuthUser | null>(["auth", "me"], (cur) => (cur ? { ...cur, pro_prompts_hidden: [...new Set([...(cur.pro_prompts_hidden ?? []), where])] } : cur));
  }, [qc, where]);
  const dismiss = useCallback(() => {
    mark();
    return backend.dismissProPrompt(where).catch(() => qc.invalidateQueries({ queryKey: ["auth", "me"] }));
  }, [mark, qc, where]);
  return { hidden, dismiss };
}
