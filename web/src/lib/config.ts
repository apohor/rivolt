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
import { useEffect, useState } from "react";
export type RuntimeConfig = {
  osrm: {
    // path is the same-origin URL prefix to use for /match, /route,
    // etc. Empty means the server didn't wire an OSRM proxy and the
    // SPA falls back to the public OSRM demo.
    path: string;
  };
  tiles: {
    // url is the same-origin URL of the served .pmtiles bundle.
    // protomaps-leaflet pulls tiles out of it via HTTP byte-range
    // reads. Empty means no self-hosted tile server is wired and
    // the SPA falls back to CARTO's public dark raster basemap.
    url: string;
    // chargersUrl is the same-origin URL of the chargers POI
    // .pmtiles archive (built from Geofabrik North America data
    // with full OSM tags preserved). Empty means the archive
    // isn't deployed and findNearestCharger falls back to the
    // basemap pois layer (less accurate, no operator/network/kW
    // info).
    chargersUrl: string;
  };
  ai: {
    // enabled is true when the install has at least one AI provider
    // configured with a working key+model. Snapshot at /api/config
    // request time -- changing settings flips this on the next page
    // reload. Used to gate AI-powered features (trip recap, future
    // weekly digest, etc.) so we don't render dead buttons.
    enabled: boolean;
  };
};

const fallback: RuntimeConfig = {
  osrm: { path: "" },
  tiles: { url: "", chargersUrl: "" },
  ai: { enabled: false },
};
let cached: RuntimeConfig = fallback;
let inflight: Promise<RuntimeConfig> | null = null;

async function loadConfig(): Promise<RuntimeConfig> {
  try {
    const r = await fetch("/api/config", { credentials: "same-origin" });
    if (!r.ok) return fallback;
    const j = (await r.json()) as {
      osrm?: { path?: string };
      tiles?: { url?: string; chargers_url?: string };
      ai?: { enabled?: boolean };
    } | null;
    return {
      osrm: { path: j?.osrm?.path ?? "" },
      tiles: {
        url: j?.tiles?.url ?? "",
        chargersUrl: j?.tiles?.chargers_url ?? "",
      },
      ai: { enabled: !!j?.ai?.enabled },
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

// tilesPMTilesURL returns the same-origin URL of the .pmtiles
// bundle, or "" when no self-hosted tile server is wired. The
// caller picks a basemap based on this: non-empty switches the
// drive map to a vector basemap via protomaps-leaflet; empty keeps
// the legacy CARTO raster path.
export function tilesPMTilesURL(): string {
  return cached.tiles.url;
}

// chargersPMTilesURL returns the same-origin URL of the chargers
// POI archive, or "" when not deployed. Used by lib/poi to query
// chargers with full OSM tags (operator, network, max kW, socket
// types) instead of the basemap's stripped-down POIs.
export function chargersPMTilesURL(): string {
  return cached.tiles.chargersUrl;
}

// aiEnabled mirrors the server-side `ai.enabled` config flag. The
// SPA gates AI-powered cards (trip recap today; weekly digest etc.
// later) on this so a fresh install with no provider key configured
// renders no dead buttons.
//
// Returns the synchronous snapshot. WARNING: this is `false` until
// ensureConfig() resolves and the module-level cache is populated.
// React components must use the `useAIEnabled` hook below so they
// re-render when the fetch completes -- a plain call to aiEnabled()
// from a component that mounts before the config resolves will
// permanently see `false` (no state subscription).
export function aiEnabled(): boolean {
  return cached.ai.enabled;
}

// useAIEnabled is the React-friendly variant of aiEnabled(). It
// awaits ensureConfig() once on mount and re-renders the caller
// when the flag flips. Use this from components -- the bare
// aiEnabled() will permanently return false in components that
// mount before the /api/config fetch resolves.
export function useAIEnabled(): boolean {
  const [enabled, setEnabled] = useState<boolean>(cached.ai.enabled);
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setEnabled(c.ai.enabled);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return enabled;
}

// Kick off the config fetch as soon as this module is imported so
// it's almost certainly resolved by the time any drive map tries
// to call snapToRoads.
void ensureConfig();
