// Runtime config is fetched once from /api/config and cached for the
// life of the SPA tab. Carries the same-origin proxy paths (Valhalla,
// PMTiles) and admin-tunable thresholds without round-tripping the
// env into the bundle at build time.
//
// Why a dedicated module instead of import.meta.env:
//   - The SPA bundle is built once and served from the same image
//     across deploys (single docker-compose, k3s, prod cluster).
//     A build-time env var would force a rebuild per deploy.
//   - Operators choose at deploy time whether to wire a self-hosted
//     Valhalla / PMTiles server. Same image, different behavior —
//     exactly what runtime config is for.
//
// The fetch is fire-and-forget at module load. Consumers read via
// `getConfig()` which returns the cached value once resolved or
// the fallback shape until then. Drive maps render the polyline
// from raw GPS first anyway and overlay snapped roads when the
// match call returns; a one-tick delay on first render is invisible.
import { useEffect, useState } from "react";
import { inDemo } from "./demo";
export type RuntimeConfig = {
  tiles: {
    // url is the same-origin URL of the served .pmtiles bundle.
    // protomaps-leaflet pulls tiles out of it via HTTP byte-range
    // reads. Empty means no self-hosted tile server is wired: maps
    // show a "tiles unavailable" notice on a plain background.
    url: string;
    // chargersUrl is the same-origin URL of the chargers POI
    // .pmtiles archive (built from Geofabrik North America data
    // with full OSM tags preserved). Empty means the archive
    // isn't deployed and findNearestCharger falls back to the
    // basemap pois layer (less accurate, no operator/network/kW
    // info).
    chargersUrl: string;
  };
  valhalla: {
    // path is the same-origin URL prefix to the Valhalla HTTP API,
    // empty when no proxy is wired. When empty the SPA renders raw
    // GPS chord polylines without snapping.
    path: string;
  };
  ai: {
    // enabled is true when the install has at least one AI provider
    // configured with a working key+model. Snapshot at /api/config
    // request time -- changing settings flips this on the next page
    // reload. Used to gate AI-powered features (trip recap, future
    // weekly digest, etc.) so we don't render dead buttons.
    enabled: boolean;
  };
  features: {
    // tripPlannerEnabled mirrors the install-wide trip-planner
    // feature flag. The SPA hides the Plan nav link and skips
    // mounting /plan when this is false; the server 404s the
    // related endpoints, so the surface is fully gone, not just
    // hidden. Admin toggles it from the Admin page.
    tripPlannerEnabled: boolean;
    // landingEnabled: install-wide switch (env RIVOLT_LANDING) for the
    // public landing page shown to signed-out visitors at /.
    landingEnabled: boolean;
    // demoEnabled: whether /demo exists; gates the landing's demo CTA.
    demoEnabled: boolean;
    // signupEnabled is false when the operator set
    // RIVOLT_SIGNUP_DISABLED: the login page hides "Create an
    // account" and /signup shows a closed notice. An older server
    // that doesn't send the field is treated as enabled.
    signupEnabled: boolean;
    // eventsEnabled mirrors RIVOLT_EVENTS: track() sends nothing unless
    // true (first-party product events: counts and enums only).
    eventsEnabled: boolean;
    // demoOnly: this deployment is the public sample-truck demo (no
    // sign-in, no Rivian): "/" lands on /demo, auth routes redirect there
    // and every sign-in CTA points at appUrl.
    demoOnly: boolean;
    // adminOnly: this deployment is the admin site (features.admin_only): the SPA shows the
    // admin-only shell, forces the new look and sends every non-admin path to /admin. An older
    // server that omits it is treated as false.
    adminOnly: boolean;
    // appUrl: origin of the real app (e.g. "https://rivolt.dev") for demo
    // CTAs. Empty means same-origin routes.
    appUrl: string;
    // iosAppUrl: the iPhone app's direct Apple link (features.ios_app_url); empty until the app is listed, and every app-promotion
    // surface stays hidden while it is.
    iosAppUrl: string;
  };
  grafana: {
    // baseUrl is the operator's Grafana origin (e.g.
    // "https://grafana.rivolt.dev"). Empty hides admin deep-links
    // into Explore. Cross-origin, so the link opens in a new tab.
    baseUrl: string;
  };
  booking: {
    // affiliateId is the operator's Booking.com partner ID (the
    // `aid` query param). Empty means we still deep-link to
    // Booking.com search results, just without capturing commission.
    affiliateId: string;
  };
  // GPS accuracy heuristic thresholds. Used by DriveDetailPage to
  // decide whether to render the "Low GPS accuracy" pill. Defaults
  // are tuned for the typical Rivian WS feed; admin can override.
  gps: {
    missingPct: number;  // fraction of samples with no LocationFixAt
    staleSec: number;    // max LocationFixAt age vs sample wall clock
    jumpCount: number;   // min implausible jumps required to flag
  };
  impersonation: {
    // disabled mirrors $RIVOLT_IMPERSONATION_DISABLED. The server
    // ignores the X-Rivolt-Impersonate header outright when this is
    // true (see internal/api/auth_mw.go); the SPA reads it to hide
    // the admin "View as" control instead of offering a dead button.
    disabled: boolean;
  };
  // rivianActions is false wherever Rivian is off (the admin-only site, the demo): the admin
  // hides "Sync" and "Refresh session". An older server that omits it is treated as true.
  rivianActions: boolean;
};

const fallback: RuntimeConfig = {
  valhalla: { path: "" },
  tiles: { url: "", chargersUrl: "" },
  ai: { enabled: false },
  features: { tripPlannerEnabled: false, landingEnabled: false, demoEnabled: false, signupEnabled: true, eventsEnabled: false, demoOnly: false, adminOnly: false, appUrl: "", iosAppUrl: "" },
  grafana: { baseUrl: "" },
  booking: { affiliateId: "" },
  gps: { missingPct: 0.4, staleSec: 300, jumpCount: 2 },
  // Fail closed like features.tripPlannerEnabled: hide the "View as"
  // control until /api/config confirms the operator hasn't disabled it.
  impersonation: { disabled: true },
  // An unreadable config means "unknown": treated as allowed, like an older server that omits it.
  rivianActions: true,
};
let cached: RuntimeConfig = fallback;
let configLoaded = false;
let inflight: Promise<RuntimeConfig> | null = null;

async function loadConfig(): Promise<RuntimeConfig> {
  try {
    const r = await fetch("/api/config", { credentials: "same-origin" });
    if (!r.ok) return fallback;
    const j = (await r.json()) as {
      valhalla?: { path?: string };
      tiles?: { url?: string; chargers_url?: string };
      ai?: { enabled?: boolean };
      features?: { trip_planner_enabled?: boolean; landing_enabled?: boolean; demo_enabled?: boolean; signup_enabled?: boolean; events_enabled?: boolean; demo_only?: boolean; admin_only?: boolean; app_url?: string; ios_app_url?: string; rivian_actions?: boolean };
      grafana?: { base_url?: string };
      booking?: { affiliate_id?: string };
      gps?: { missing_pct?: number; stale_sec?: number; jump_count?: number };
      impersonation?: { disabled?: boolean };
    } | null;
    return {
      valhalla: { path: j?.valhalla?.path ?? "" },
      tiles: {
        url: j?.tiles?.url ?? "",
        chargersUrl: j?.tiles?.chargers_url ?? "",
      },
      ai: { enabled: !!j?.ai?.enabled },
      features: {
        tripPlannerEnabled: !!j?.features?.trip_planner_enabled,
        landingEnabled: !!j?.features?.landing_enabled,
        demoEnabled: !!j?.features?.demo_enabled,
        signupEnabled: j?.features?.signup_enabled !== false,
        eventsEnabled: j?.features?.events_enabled === true,
        demoOnly: j?.features?.demo_only === true,
        adminOnly: j?.features?.admin_only === true,
        appUrl: (j?.features?.app_url ?? "").replace(/\/+$/, ""),
        iosAppUrl: j?.features?.ios_app_url ?? "",
      },
      grafana: { baseUrl: j?.grafana?.base_url ?? "" },
      booking: { affiliateId: j?.booking?.affiliate_id ?? "" },
      gps: {
        missingPct: typeof j?.gps?.missing_pct === "number" ? j.gps.missing_pct : fallback.gps.missingPct,
        staleSec: typeof j?.gps?.stale_sec === "number" ? j.gps.stale_sec : fallback.gps.staleSec,
        jumpCount: typeof j?.gps?.jump_count === "number" ? j.gps.jump_count : fallback.gps.jumpCount,
      },
      impersonation: { disabled: !!j?.impersonation?.disabled },
      // false wherever Rivian is off (the admin-only site, the demo); absent means allowed.
      rivianActions: j?.features?.rivian_actions !== false,
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
      configLoaded = true;
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

// valhallaBase returns the base URL for the Valhalla HTTP API, or
// "" when no proxy is wired. There's no public-demo fallback —
// Valhalla is only available when self-hosted.
export function valhallaBase(): string {
  // No anonymous routing calls from the demo: the stored GPS polyline is
  // drawn as recorded (no map-matching, no gap-filling routes).
  if (inDemo()) return "";
  return cached.valhalla.path;
}

// tilesPMTilesURL returns the same-origin URL of the .pmtiles
// bundle, or "" when no self-hosted tile server is wired. The
// caller picks a basemap based on this: non-empty switches the
// drive map to a vector basemap via protomaps-leaflet; empty keeps
// the "tiles unavailable" notice.
export function tilesPMTilesURL(): string {
  // The demo uses the same self-hosted basemap archive as the signed-in
  // app (the server allows the basemap for the demo). CARTO's public
  // raster needs an API key now and renders placeholder tiles, so it is
  // not a fallback for the demo.
  return cached.tiles.url;
}

// bookingAffiliateID returns the operator's Booking.com partner
// ID, or "" when not configured. Empty just means hotel deep-links
// don't carry an affiliate tag - they still work, the operator
// just doesn't capture commission on user bookings.
export function bookingAffiliateID(): string {
  return getConfig().booking.affiliateId;
}

// grafanaBaseURL returns the operator's Grafana origin or "" when
// none is wired. Admin surfaces deep-link into Explore via this.
export function grafanaBaseURL(): string {
  return cached.grafana.baseUrl;
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
  // AI calls cost money: never from the public demo.
  if (inDemo()) return false;
  return cached.ai.enabled;
}

// useSignupEnabled resolves to null until /api/config has loaded, then
// to features.signupEnabled. Callers render nothing signup-related
// while it is null, so a closed server never flashes the link.
export function useSignupEnabled(): boolean | null {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setEnabled(c.features.signupEnabled);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return enabled;
}

// useIosAppUrl: the iPhone app's Apple link once /api/config has loaded, or "" (not listed yet, the demo, or still loading).
export function useIosAppUrl(): string {
  const [url, setUrl] = useState("");
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setUrl(inDemo() ? "" : c.features.iosAppUrl);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return url;
}

// useAIEnabled is the React-friendly variant of aiEnabled(). It
// awaits ensureConfig() once on mount and re-renders the caller
// when the flag flips. Use this from components -- the bare
// aiEnabled() will permanently return false in components that
// mount before the /api/config fetch resolves.
export function useAIEnabled(): boolean {
  const [enabled, setEnabled] = useState<boolean>(aiEnabled());
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setEnabled(inDemo() ? false : c.ai.enabled);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return enabled;
}

// useDemoEnabled: whether the public sample-truck demo exists on this
// install (features.demo_enabled). False until /api/config loads.
export function useDemoEnabled(): boolean {
  const [enabled, setEnabled] = useState<boolean>(cached.features.demoEnabled);
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setEnabled(c.features.demoEnabled);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return enabled;
}

// useGPSThresholds returns the admin-configurable thresholds the
// drive detail page uses to decide whether to render the "Low GPS
// accuracy" pill. Components that mount before /api/config resolves
// re-render with the fetched values when ready.
export function useGPSThresholds(): RuntimeConfig["gps"] {
  const [g, setG] = useState<RuntimeConfig["gps"]>(cached.gps);
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setG(c.gps);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return g;
}

// useTripPlannerEnabled is the React-friendly accessor for the
// trip-planner feature flag. Same shape as useAIEnabled — components
// that mount before /api/config resolves will re-render when the
// fetch completes.
export function useTripPlannerEnabled(): boolean {
  const [enabled, setEnabled] = useState<boolean>(
    cached.features.tripPlannerEnabled,
  );
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setEnabled(c.features.tripPlannerEnabled);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return enabled;
}

// useImpersonationDisabled is the React-friendly accessor for the
// admin impersonation kill switch. Same shape as
// useTripPlannerEnabled — components that mount before /api/config
// resolves start from the fail-closed fallback (disabled=true) and
// re-render once the real value arrives.
export function useImpersonationDisabled(): boolean {
  const [disabled, setDisabled] = useState<boolean>(
    cached.impersonation.disabled,
  );
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setDisabled(c.impersonation.disabled);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return disabled;
}

// useRivianActions: whether this site can act on Rivian (sync a user's vehicles, refresh a session).
// False wherever Rivian is off (the admin-only site, the demo). Until /api/config has answered it is false, so those controls never
// flash up on a site that cannot run them; once it answers, absent means true.
export function useRivianActions(): boolean {
  const [on, setOn] = useState<boolean>(configLoaded ? cached.rivianActions : false);
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setOn(c.rivianActions);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return on;
}

// Kick off the config fetch as soon as this module is imported so
// it's almost certainly resolved by the time any drive map tries
// to call snapToRoads.
void ensureConfig();

// useDemoOnly resolves {loading, only}: whether this deployment is the
// public demo (features.demo_only). Callers wait for loading to finish
// before deciding where "/" goes.
export function useDemoOnly(): { loading: boolean; only: boolean } {
  const [state, setState] = useState<{ loading: boolean; only: boolean }>({
    loading: cached === fallback,
    only: cached.features.demoOnly,
  });
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setState({ loading: false, only: c.features.demoOnly });
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return state;
}

// useAdminOnly resolves {loading, only}: whether this deployment is the admin site
// (features.admin_only). Callers wait for loading to finish before mounting anything that fetches.
export function useAdminOnly(): { loading: boolean; only: boolean } {
  const [state, setState] = useState<{ loading: boolean; only: boolean }>({
    loading: cached === fallback,
    only: cached.features.adminOnly,
  });
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setState({ loading: false, only: c.features.adminOnly });
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return state;
}

// useAppUrl is the origin demo CTAs should send visitors to ("" = same origin).
export function useAppUrl(): string {
  const [url, setUrl] = useState<string>(cached.features.appUrl);
  useEffect(() => {
    let cancelled = false;
    ensureConfig().then((c) => {
      if (!cancelled) setUrl(c.features.appUrl);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return url;
}
