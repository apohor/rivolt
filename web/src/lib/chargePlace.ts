import type { Charge, ChargeCluster, ChargeClusterLabel } from "./api";

// One label for a charge on every page (Now, Activity, Charges, the charge
// page), from the server's classification: the ledger's kind and place,
// the same rules as Money. Fast is a DC fast charge wherever it was; Home
// and Away are AC charges by place. The cluster label (Home / Public /
// Fast) is only a fallback, for rows the server couldn't classify or
// whose place is unknown (no home set).
export type ChargePlaceLabel = "Home" | "Away" | "Fast" | "";

export function chargePlaceLabel(
  c: Pick<Charge, "kind" | "place">,
  cluster?: ChargeClusterLabel,
): ChargePlaceLabel {
  if (c.kind === "dcfc") return "Fast";
  if (c.kind === "ac") {
    if (c.place === "home") return "Home";
    if (c.place === "away") return "Away";
    // Kind is known, so the cluster may only settle the place.
    return cluster === "Home" ? "Home" : cluster === "Public" ? "Away" : "";
  }
  if (cluster === "Home") return "Home";
  if (cluster === "Public") return "Away";
  if (cluster === "Fast") return "Fast";
  return "";
}

// chargePlaceLabels labels each charge by ID.
export function chargePlaceLabels(
  charges: Pick<Charge, "ID" | "kind" | "place">[],
  clusters: ChargeCluster[],
): Map<string, ChargePlaceLabel> {
  const byID = new Map<string, ChargeClusterLabel>();
  for (const cl of clusters) {
    const label: ChargeClusterLabel =
      cl.label === "Home" || cl.label === "Public" || cl.label === "Fast" ? cl.label : "";
    for (const id of cl.member_ids) byID.set(id, label);
  }
  const out = new Map<string, ChargePlaceLabel>();
  for (const c of charges) out.set(c.ID, chargePlaceLabel(c, byID.get(c.ID)));
  return out;
}
