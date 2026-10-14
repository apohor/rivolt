// Decides whether "/" shows the public landing page. Only for signed-
// out visitors, and only when the install turned it on
// (RIVOLT_LANDING). Everything else keeps today's behaviour: signed-in
// users get the app, signed-out users the usual login redirect.

import { useEffect, useState } from "react";
import { ensureConfig } from "./config";

export type LandingGate = "off" | "loading" | "landing";

export async function resolveLanding(): Promise<"off" | "landing"> {
  try {
    const cfg = await ensureConfig();
    if (!cfg.features.landingEnabled) return "off";
    // Only a 401 means "signed out". A 404 is auth disabled (self-host
    // without auth), where the app itself must stay reachable at "/".
    const r = await fetch("/api/auth/me", { credentials: "same-origin" });
    return r.status === 401 ? "landing" : "off";
  } catch {
    return "off";
  }
}

export function useLandingGate(pathname: string): LandingGate {
  const atRoot = pathname === "/";
  const [state, setState] = useState<"off" | "landing" | null>(null);
  useEffect(() => {
    if (!atRoot) return;
    let cancelled = false;
    resolveLanding().then((r) => {
      if (!cancelled) setState(r);
    });
    return () => {
      cancelled = true;
    };
  }, [atRoot]);
  if (!atRoot) return "off";
  return state ?? "loading";
}
