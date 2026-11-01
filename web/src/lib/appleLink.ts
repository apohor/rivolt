import { ApiError } from "./api";
import { APPLE_LINK_COPY } from "../content/appleLinkCopy";

const returned = APPLE_LINK_COPY.returned;

// returnMessage is what to tell the person when Apple sent them back to Settings with ?apple=<code>; null for a code we don't know (nothing is shown).
export function returnMessage(code: string | null): { text: string; bad: boolean } | null {
  if (!code) return null;
  const text = returned[code];
  if (!text) return null;
  return { text, bad: code !== "linked" };
}

// failureMessage is what to tell the person when connecting or disconnecting did not work. Never the server's own text.
export function failureMessage(e: unknown): string {
  const f = APPLE_LINK_COPY.failures;
  if (e instanceof ApiError) {
    const code = (e.body as { error?: string } | null | undefined)?.error ?? "";
    if (e.status === 429) return f.too_many;
    if (code === "password_incorrect") return f.password_incorrect;
    if (code === "password_required") return f.password_required;
    if (code === "password_needed_to_link") return f.password_needed_to_link;
    if (code === "apple_not_available") return f.apple_not_available;
    if (code === "not_linked") return f.not_linked;
    if (e.status === 403) return f.not_here;
  }
  return f.generic;
}
