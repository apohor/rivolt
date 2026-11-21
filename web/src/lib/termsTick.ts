// The Terms box ticked on the sign-up screen (design key signup_v2), carried across the sign-in redirect. Only a note that the box was
// ticked for one version; the acceptance itself is recorded by POST /api/terms/accept for the signed-in user (TermsGate does it), never
// from the account being created. Per browser, short-lived, one-shot: reading it removes it.
const KEY = "rivolt.terms_ticked";
const TTL_MS = 15 * 60_000;

export function rememberTermsTick(version: string): void {
  try {
    if (version) localStorage.setItem(KEY, JSON.stringify({ version, at: Date.now() }));
  } catch {
    /* storage blocked: the "One more step" screen asks instead */
  }
}

// takeTermsTick: true once if a fresh note for exactly this version exists; any note is removed.
export function takeTermsTick(version: string): boolean {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw === null) return false;
    localStorage.removeItem(KEY);
    const n = JSON.parse(raw) as { version?: unknown; at?: unknown };
    return n.version === version && typeof n.at === "number" && Date.now() - n.at >= 0 && Date.now() - n.at < TTL_MS;
  } catch {
    return false;
  }
}
