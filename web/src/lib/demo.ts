// Public sample-truck demo helpers. The demo lives under /demo (signed
// out, read-only); its detail pages are /demo/charges/:id and
// /demo/drives/:id, which reuse the real detail components in a
// read-only mode.

export function inDemo(): boolean {
  return typeof window !== "undefined" && window.location.pathname.startsWith("/demo");
}

// The demo is fixed to the sample truck's home zone so its month totals,
// day groups and screenshots are the same for every viewer.
export const DEMO_TZ = "America/Los_Angeles";

export type DetailKind = "charges" | "drives";

// detailPath is where a charge or drive detail lives right now: under
// /demo while in the demo, the signed-in route otherwise.
export function detailPath(kind: DetailKind, id: string): string {
  return `${inDemo() ? "/demo" : ""}/${kind}/${encodeURIComponent(id)}`;
}

// listPath is the "back to all …" target: the demo's own page in the
// demo (its tab is picked from the query string).
export function listPath(kind: DetailKind): string {
  if (!inDemo()) return `/${kind}`;
  return `/demo?tab=${kind === "charges" ? "money" : "activity"}`;
}
