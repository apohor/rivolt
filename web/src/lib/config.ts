// Runtime config is fetched once from /api/config and cached for the
// life of the SPA tab. Today it carries the OSRM same-origin proxy
// path; future knobs (tile server URL, feature flags) can ride here
// without round-tripping the env into the bundle at build time.
//
// Why a dedicated module instead of import.meta.env:
//   - The SPA bundle is built once and served from the same image
//     across deploys (single docker-compose, k3s, prod cluster).
//     A build-time env var would force a rebuild per deploy.
//   - Operators choose at deploy time whether to wire a self-hosted
//     OSRM. Same image, different behavior — exactly what runtime
//     config is for.
//
// The fetch is fire-and-forget at module load. Consumers read via
// `getConfig()` which returns the cached value once resolved or
// the fallback shape until then. Drive maps render the polyline
// from raw GPS first anyway and overlay snapped roads when the
// match call returns; a one-tick delay on first render is invisible.

export type RuntimeConfig = {
  osrm: {
    // path is the same-origin URL prefix to use for /match, /route,
    // etc. Empty means the server didn't wire an OSRM proxy and the
    // SPA falls back to the public OSRM demo.
    path: string;
  };
};

const fallback: RuntimeConfig = { osrm: { path: "" } };
let cached: RuntimeConfig = fallback;
let inflight: Promise<RuntimeConfig> | null = null;

async function loadConfig(): Promise<RuntimeConfig> {
  try {
    const r = await fetch("/api/config", { credentials: "same-origin" });
    if (!r.ok) return fallback;
    const j = (await r.json()) as Partial<RuntimeConfig> | null;
    return {
      osrm: { path: j?.osrm?.path ?? "" },
    };
  } catch {
    return fallback;
  }
}

// ensureConfig kicks off the load (idempotent) and resolves to the
// canonical config. Safe to call from many places concurrently;
// they all share the same in-flight promise.
export function ensureConfig(): Promise<RuntimeConfig> {
  if (!inflight) {
    inflight = loadConfig().then((c) => {
      cached = c;
      return c;
    });
  }
  return inflight;
}

// getConfig returns the last-loaded config, or the fallback shape
// until the load resolves. Callers that need the "real" answer
// must await ensureConfig() first.
export function getConfig(): RuntimeConfig {
  return cached;
}

// osrmBase returns the base URL for OSRM HTTP endpoints. When the
// server has wired the same-origin proxy this is "/api/maps/osrm";
// otherwise it falls back to the public demo. /match etc. paths
// are appended by the caller.
export function osrmBase(): string {
  return cached.osrm.path || "https://router.project-osrm.org";
}

// Kick off the config fetch as soon as this module is imported so
// it's almost certainly resolved by the time any drive map tries
// to call snapToRoads.
void ensureConfig();
