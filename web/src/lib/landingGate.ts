// Decides whether "/" shows the public landing page. Only for signed-
// out visitors, and only when the install turned it on
// (RIVOLT_LANDING). Everything else keeps today's behaviour: signed-in
// users get the app, signed-out users the usual login redirect.

import { useEffect, useState } from "react";
import { backend } from "./api";
import { ensureConfig } from "./config";

export type LandingGate = "off" | "loading" | "landing";

export async function resolveLanding(): Promise<"off" | "landing"> {
  try {
    const cfg = await ensureConfig();
    if (!cfg.features.landingEnabled) return "off";
    const me = await backend.whoami();
    return me ? "off" : "landing";
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
