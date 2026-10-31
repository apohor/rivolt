// The charger popup's pure parts: the OSM `socket:*` labels, the OpenStreetMap link and the spec rows (HTML, all data escaped). Shared by
// the charge map; free of any map library.
import { escapeHTML } from "../lib/escapeHtml";
import type { POI } from "../lib/poi";

// Friendly labels for the OSM `socket:*` keys we care about. Keys
// not in this map fall through to a tidied-up version of the raw
// tag (underscores → spaces, title-cased).
const SOCKET_LABELS: Record<string, string> = {
  type1: "J1772",
  type1_combo: "CCS1",
  type2: "Type 2",
  type2_combo: "CCS2",
  type2_cable: "Type 2 (cable)",
  chademo: "CHAdeMO",
  tesla_supercharger: "Supercharger",
  tesla_supercharger_ccs: "Supercharger (CCS)",
  tesla_destination: "Tesla Destination",
  nacs: "NACS",
};

function prettySocket(s: string): string {
  if (SOCKET_LABELS[s]) return SOCKET_LABELS[s];
  return s
    .split("_")
    .map((w) => (w ? w[0].toUpperCase() + w.slice(1) : w))
    .join(" ");
}

// Build the OSM "view this feature" link. Prefers the canonical
// /node/<id> URL when tippecanoe preserved the OSM id; falls back
// to the lat/lon-anchored marker URL otherwise.
export function osmLinkURL(poi: POI): string {
  if (poi.osmId && /^(node|way|relation)\/\d+$/.test(poi.osmId)) {
    return `https://www.openstreetmap.org/${poi.osmId}`;
  }
  return `https://www.openstreetmap.org/?mlat=${poi.lat}&mlon=${poi.lon}#map=19/${poi.lat}/${poi.lon}`;
}

// Render the charger spec list as inner HTML for the popup. Only
// emits rows for fields that are present, so a sparsely-tagged
// charger doesn't show empty placeholders. Returns "" when nothing
// is known (basemap fallback path) — the popup falls back to just
// the name + snap-distance footer.
export function chargerSpecListHTML(poi: POI): string {
  if (poi.source !== "chargers") {
    // Basemap fallback strips POI tags down to name only. Surface
    // a "limited data" hint so the user knows we matched something,
    // it just doesn't carry network/power/socket metadata.
    return (
      '<div style="color:#a3a3a3;font-size:11px;line-height:1.5;font-style:italic">' +
      "Limited data — basemap snap (no network or power info)" +
      "</div>"
    );
  }
  const rows: string[] = [];
  const row = (label: string, value: string) =>
    rows.push(
      `<div style="display:flex;justify-content:space-between;gap:8px;font-size:11px;line-height:1.5">` +
        `<span style="color:#a3a3a3">${escapeHTML(label)}</span>` +
        `<span style="color:#fafafa;text-align:right">${value}</span>` +
        `</div>`,
    );
  // Network beats operator when both are present — most users think
  // of "EVgo" or "Electrify America" first, "operated by ..." second.
  const network = poi.network ?? poi.brand;
  if (network) row("Network", escapeHTML(network));
  if (poi.operator && poi.operator !== network) {
    row("Operator", escapeHTML(poi.operator));
  }
  if (poi.maxPowerKW != null && poi.maxPowerKW > 0) {
    // Format as integer kW for ≥1 kW values; trailing .x rarely
    // matters and clutters the popup.
    const kw = poi.maxPowerKW >= 10
      ? Math.round(poi.maxPowerKW).toString()
      : poi.maxPowerKW.toFixed(1);
    row("Max power", `${kw} kW`);
  }
  if (poi.capacity && poi.capacity > 0) {
    row("Stalls", String(poi.capacity));
  }
  if (poi.socketTypes && poi.socketTypes.length > 0) {
    const labels = poi.socketTypes.map(prettySocket).join(", ");
    row("Connectors", escapeHTML(labels));
    // Tesla Superchargers expose the NACS plug. Rivians need a
    // NACS↔CCS1 adapter to use them, unless the station also
    // advertises CCS1 (Magic Dock retrofits, type1_combo) — in
    // which case there's a native CCS1 cable on-site and no
    // adapter is needed. Same logic the trip planner uses for
    // its "Tesla adapter required" hint.
    const hasTesla = poi.socketTypes.includes("tesla_supercharger");
    const hasCCS1 = poi.socketTypes.includes("type1_combo");
    if (hasTesla && !hasCCS1) {
      rows.push(
        `<div style="font-size:11px;line-height:1.5;color:#fbbf24;margin-top:2px">` +
          "Tesla adapter required" +
          `</div>`,
      );
    }
  }
  if (poi.fee) {
    const v = poi.fee.toLowerCase();
    const friendly =
      v === "yes" ? "Paid" : v === "no" ? "Free" : escapeHTML(poi.fee);
    row("Fee", friendly);
  }
  if (poi.openingHours) {
    row("Hours", escapeHTML(poi.openingHours));
  }
  if (rows.length === 0) return "";
  return (
    `<div style="margin:6px 0;padding:6px 0;border-top:1px solid #262626;border-bottom:1px solid #262626">` +
    rows.join("") +
    `</div>`
  );
}

