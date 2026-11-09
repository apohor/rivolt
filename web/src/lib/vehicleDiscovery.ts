import copy from "./vehicleDiscovery.json";

// What the "My vehicles" area says while a linked Rivian account has no vehicle on file, from GET /api/vehicles/discovery. The server's answer, in its own
// order of precedence: unlinked, reauth, found, none, looking. A failed lookup is "looking" (the server retries it), and so is a 503. The iPhone app says the
// same words (ios/design/vehicle-discovery.json is generated from vehicleDiscovery.json).
export type DiscoveryState = "unlinked" | "reauth" | "found" | "none" | "looking";

export const DISCOVERY_COPY = copy;

export type DiscoveryView = { kind: "looking" | "none" | "reauth"; title: string; body: string; action?: string };

// discoveryView is what to show for the server's answer: nothing (null) for the states the page already handles (not linked: the connect prompt; found:
// the vehicles themselves) and when the server says nothing (the feature is off there, or the answer is not known yet).
export function discoveryView(state: DiscoveryState | null | undefined): DiscoveryView | null {
  switch (state) {
    case "looking":
      return { kind: "looking", ...copy.looking };
    case "none":
      return { kind: "none", ...copy.none };
    case "reauth":
      return { kind: "reauth", ...copy.reauth };
    default:
      return null;
  }
}

// discoveryStateOf turns what the request gave back into a state: the answer, a 503 as "looking", and anything else (a 404 where the feature is off,
// a network failure) as "no word", so the page keeps its older wording.
export function discoveryStateOf(answer: { state?: string } | undefined, errorStatus?: number): DiscoveryState | null {
  const s = answer?.state;
  if (s === "unlinked" || s === "reauth" || s === "found" || s === "none" || s === "looking") return s;
  return errorStatus === 503 ? "looking" : null;
}
