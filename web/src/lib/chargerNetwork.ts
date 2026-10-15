// Which network a charge really was on vs which charger the map snapped
// to. At shared sites (RAN next to Tesla Superchargers, EA next to a
// Supercharger) the nearest OSM charging node can be a different
// network's, and labelling the pin with it is wrong. The ledger already
// knows the real network, so it wins when the two disagree.

export type NetworkKey = "tesla" | "tesla_destination" | "rivian" | "rivian_waypoints" | "electrify_america" | "evgo" | "chargepoint" | "ionna" | "shell_recharge" | null;

const DISPLAY: Record<Exclude<NetworkKey, null>, string> = {
  tesla: "Tesla Supercharger",
  tesla_destination: "Tesla Destination",
  rivian: "Rivian Adventure Network",
  rivian_waypoints: "Rivian Waypoints",
  electrify_america: "Electrify America",
  evgo: "EVgo",
  chargepoint: "ChargePoint",
  ionna: "IONNA",
  shell_recharge: "Shell Recharge",
};

// networkKey reads a free-form network, operator, brand or name string.
export function networkKey(...parts: (string | null | undefined)[]): NetworkKey {
  const s = parts
    .filter((p): p is string => !!p)
    .join(" ")
    .toLowerCase();
  if (!s.trim()) return null;
  // AC sub-brands first: they share the parent brand's name but are L2.
  if (/\btesla\b.*\bdestination\b|\bdestination\b.*\btesla\b/.test(s)) return "tesla_destination";
  if (/\brivian\b.*\bwaypoints?\b|\bwaypoints?\b/.test(s)) return "rivian_waypoints";
  if (/\btesla\b|supercharger/.test(s)) return "tesla";
  if (/\brivian\b|adventure network|\bran\b/.test(s)) return "rivian";
  if (/electrify\s*america|\bea\b/.test(s)) return "electrify_america";
  if (/\bev\s*go\b|evgo/.test(s)) return "evgo";
  if (/charge\s*point|chargepoint/.test(s)) return "chargepoint";
  if (/\bionna\b/.test(s)) return "ionna";
  if (/shell\s*recharge|\bshell\b/.test(s)) return "shell_recharge";
  return null;
}

// displayNetwork is what a pin says: a friendly canonical name, else the
// ledger's own string.
export function displayNetwork(known: string): string {
  const k = networkKey(known);
  return k ? DISPLAY[k] : known.trim();
}

export type PoiLike = {
  name?: string;
  operator?: string;
  network?: string;
  brand?: string;
  maxPowerKW?: number;
};

// Networks that only run DC fast chargers. A level-2 (AC) session is never
// at one of their nodes, so it must not snap to one even when it is the
// nearest charging node (a hotel L2 beside a Supercharger).
const DC_ONLY: ReadonlySet<NetworkKey> = new Set<NetworkKey>([
  "tesla",
  "rivian",
  "electrify_america",
  "evgo",
  "ionna",
]);

export function isDcOnlyNode(poi: PoiLike | null): boolean {
  if (!poi) return false;
  if (typeof poi.maxPowerKW === "number" && poi.maxPowerKW >= 50) return true;
  return DC_ONLY.has(networkKey(poi.network, poi.operator, poi.brand, poi.name));
}

export type PinChoice =
  // Use the snapped charger as-is.
  | { snap: true; label: string }
  // Don't snap: label the charge's own location with the known network.
  | { snap: false; label: string };

const UNKNOWN_NETWORKS = new Set(["", "unknown", "other", "n/a"]);

// choosePin decides what the charge pin says. Never a wrong brand:
// - a non-DC session never snaps to a DC-only node (Supercharger, RAN, ...);
// - the ledger's network is unknown: keep today's snap and OSM name, or
//   say "DC fast charger" only when the charge is DC, else "Charging station";
// - the snapped charger agrees: keep the snap;
// - it disagrees, or can't be verified (no network info on the node):
//   label the charge's own location with the known network.
export function choosePin(
  poi: PoiLike | null,
  knownNetwork: string | null | undefined,
  isDC = false,
): PinChoice {
  const known = (knownNetwork ?? "").trim();
  // A non-DC session never snaps to a DC-only node: label the charge's own
  // location, with the ledger's network when it has one.
  if (!isDC && isDcOnlyNode(poi)) {
    const unknown = UNKNOWN_NETWORKS.has(known.toLowerCase());
    return { snap: false, label: unknown ? "Charging station" : displayNetwork(known) };
  }
  const snappedLabel = poi?.name || "Charging station";
  if (UNKNOWN_NETWORKS.has(known.toLowerCase())) {
    return poi ? { snap: true, label: snappedLabel } : { snap: false, label: isDC ? "DC fast charger" : "Charging station" };
  }
  if (!poi) return { snap: false, label: displayNetwork(known) };
  const want = networkKey(known);
  const got = networkKey(poi.network, poi.operator, poi.brand, poi.name);
  if (want && got && want === got) return { snap: true, label: snappedLabel };
  if (!want && got == null) return { snap: true, label: snappedLabel }; // nothing to compare either way
  return { snap: false, label: displayNetwork(known) };
}
