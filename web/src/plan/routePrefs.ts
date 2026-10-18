// Route preferences: road types a trip should avoid. They apply to every day
// of a multi-day trip and are saved with the trip's inputs. The server takes
// `route_preferences` only when at least one is on, so a trip with none sends
// exactly the request it always did.

export type RoutePrefs = { avoidTolls: boolean; avoidFerries: boolean; avoidHighways: boolean };

export const noRoutePrefs: RoutePrefs = { avoidTolls: false, avoidFerries: false, avoidHighways: false };

// The API shape (snake_case), shared by the plan request and the saved inputs.
export type RoutePrefsWire = { avoid_tolls: boolean; avoid_ferries: boolean; avoid_highways: boolean };

export function anyRoutePref(p: RoutePrefs): boolean {
  return p.avoidTolls || p.avoidFerries || p.avoidHighways;
}

// routePrefsWire is what to send: undefined unless something is avoided.
export function routePrefsWire(p: RoutePrefs): RoutePrefsWire | undefined {
  if (!anyRoutePref(p)) return undefined;
  return { avoid_tolls: p.avoidTolls, avoid_ferries: p.avoidFerries, avoid_highways: p.avoidHighways };
}

// routePrefsFromInputs reads a saved trip's inputs (anything that was stored,
// so every field is checked). Missing or malformed means no preferences.
export function routePrefsFromInputs(inputs: unknown): RoutePrefs {
  if (!inputs || typeof inputs !== "object") return noRoutePrefs;
  const raw = (inputs as Record<string, unknown>).route_preferences;
  if (!raw || typeof raw !== "object") return noRoutePrefs;
  const r = raw as Record<string, unknown>;
  return {
    avoidTolls: r.avoid_tolls === true,
    avoidFerries: r.avoid_ferries === true,
    avoidHighways: r.avoid_highways === true,
  };
}

// avoidSummary names what is avoided: "tolls and highways", "" when nothing.
export function avoidSummary(p: RoutePrefs): string {
  const names = [p.avoidTolls && "tolls", p.avoidFerries && "ferries", p.avoidHighways && "highways"].filter(
    (n): n is string => typeof n === "string",
  );
  if (names.length <= 1) return names[0] ?? "";
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}
