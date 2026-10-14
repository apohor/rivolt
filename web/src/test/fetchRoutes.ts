import { vi } from "vitest";

// stubFetchRoutes installs a fetch mock that answers by URL path
// prefix. Unmatched paths 404, which is what most of the SPA treats
// as "feature off / not there".
export function stubFetchRoutes(routes: Record<string, { status?: number; body: unknown }>) {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input.toString();
    const path = url.split("?")[0];
    const hit = Object.entries(routes)
      .sort((a, b) => b[0].length - a[0].length)
      .find(([p]) => path === p || path.startsWith(p + "/") || (p.endsWith("*") && path.startsWith(p.slice(0, -1))));
    const status = hit ? (hit[1].status ?? 200) : 404;
    const body = hit ? hit[1].body : "not found";
    return {
      ok: status >= 200 && status < 300,
      status,
      headers: { get: () => "application/json" },
      text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
      json: async () => body,
    } as unknown as Response;
  });
  vi.stubGlobal("fetch", fn);
  return fn;
}
