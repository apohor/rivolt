// DCFC auto-network pricing (contract dcfc-auto-network-contract v1).
// The backend identifies the network at charge close and prices it at the
// user's rate; the UI asks the owner to confirm or change it. Every field
// is null until the dcfc_auto_network flag is on for the user, so these
// helpers treat null as "feature off" and render nothing new.

export type NetworkState = "estimated" | "needs_confirmation" | "confirmed";

export type NetworkEvidence = {
  v?: number;
  source?: "rivian_charger" | "site_memory" | "archive" | "none";
  candidates?: { network: string; slug?: string; dist_m: number }[];
  osm?: { network?: string; agree?: boolean } | null;
  pos?: string;
  rate_basis?: "member" | "guest" | "override";
};

type Stated = { network_state?: NetworkState | null };

// Open means the owner still has something to look at.
export function isOpenNetworkState(s: NetworkState | null | undefined): boolean {
  return s === "estimated" || s === "needs_confirmation";
}

export function hasOpenNetworkState(r: Stated & { sessions?: Stated[] }): boolean {
  if (isOpenNetworkState(r.network_state)) return true;
  return !!r.sessions?.some((x) => isOpenNetworkState(x.network_state));
}

function metres(m: number): string {
  return m >= 1000 ? `${(m / 1000).toFixed(1)} km` : `${Math.round(m)} m`;
}

// whyLine: "Nearest: Tesla Supercharger, 38 m; also Rivian Adventure
// Network, 112 m". Distances are metres at the charge's plug-in position.
export function whyLine(ev: NetworkEvidence | null | undefined): string | null {
  if (!ev) return null;
  if (ev.source === "rivian_charger") return "Rivian Adventure Network charger";
  if (ev.source === "site_memory") return "You confirmed this network at this location before";
  const c = ev.candidates ?? [];
  if (c.length === 0) return null;
  const [first, ...rest] = c;
  let s = `Nearest: ${first.network}, ${metres(first.dist_m)}`;
  if (rest.length > 0) s += `; also ${rest.map((x) => `${x.network}, ${metres(x.dist_m)}`).join(", ")}`;
  if (ev.osm && ev.osm.agree === false && ev.osm.network) s += `. The map lists it as ${ev.osm.network}`;
  return s;
}

export function rateBasisLabel(b: NetworkEvidence["rate_basis"]): string | null {
  return b === "member" ? "member" : b === "guest" ? "guest" : b === "override" ? "your price" : null;
}
