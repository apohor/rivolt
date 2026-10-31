// Pure helpers of the planner's trip map, kept free of any map library: the waypoint tooltip, the charger and campground popup HTML (all
// data escaped), the Booking.com deep link, the Valhalla multi-stop route and the polyline6 decoder.
import { escapeHTML } from "../lib/escapeHtml";
import { bookingAffiliateID, valhallaBase } from "../lib/config";
import type { POI } from "../lib/poi";
import type { HookupId } from "../plan/hookups";
import { CAMP_WARNING, RIDB_CREDIT, outletLabel, sitesLine, campPowerLine } from "../plan/stays";
import type { PlannedWaypoint } from "../lib/api";
import { MAP_CAPTION_PX } from "../lib/mapCaption";

// PreviewResult mirrors the shape returned by the trip planner page's onPreviewStop callback (the popup shows "+18 min · +$4 · arrives 7% higher").
export type PreviewResult = {
  delta_total_time_sec: number;
  delta_arrival_soc_pct: number;
  delta_stop_count: number;
  delta_cost: number;
  currency: string;
  stops_kept: number;
  stops_dropped: number;
  stops_added: number;
  substituted_for?: string;
};

export function waypointTooltip(w: PlannedWaypoint, i: number, total: number): string {
  const isFirst = i === 0;
  const isLast = i === total - 1;
  const heading = isFirst
    ? "Origin"
    : isLast
      ? "Destination"
      : w.Name || "Charging stop";
  const lines: string[] = [
    `<div style="font:12px/1.35 ui-sans-serif,system-ui;color:#fafafa">`,
    `<div style="color:#34d399;font-weight:600">${escapeHTML(heading)}</div>`,
  ];
  if (!isFirst && !isLast) {
    if (w.MaxPowerKW > 0) {
      lines.push(`<div>Up to ${w.MaxPowerKW.toFixed(0)} kW</div>`);
    }
    if (w.ChargeDurationSec > 0) {
      lines.push(
        `<div>Charge ${Math.round(w.ChargeDurationSec / 60)} min · ${w.ArrivalSoC.toFixed(0)}% → ${w.DepartureSoC.toFixed(0)}%</div>`,
      );
    }
    // Only surface the adapter warning when the station name actually
    // looks like a Tesla one. Rivian's planTrip2 has been observed
    // flagging adapterRequired=true on Rivian Adventure Network /
    // EA / EVgo stops, which is nonsensical — those connectors are
    // CCS-native for an R1. The name pattern Rivian uses for
    // Supercharger picks is "<location> [Tesla]" (sometimes
    // "Tesla Supercharger" in older responses); fall back to
    // suppressing the line otherwise.
    if (w.AdapterRequired && /\bTesla\b|\[Tesla\]/i.test(w.Name)) {
      lines.push(`<div style="color:#fbbf24">Tesla adapter required</div>`);
    }
  } else if (isLast) {
    lines.push(`<div>Arrive at ${w.ArrivalSoC.toFixed(0)}%</div>`);
  } else {
    lines.push(`<div>Depart at ${w.DepartureSoC.toFixed(0)}%</div>`);
  }
  lines.push(`</div>`);
  return lines.join("");
}


// Marker colors:
//   - dcfc:  cyan,   the default highway DCFC stop
//   - l2:    lime,   plain Level-2 stop
//   - hotel: violet, L2 at an overnight venue (HOTEL/MOTEL/INN/
//           B_AND_B/RESORT per NREL facility_type)
export const CHARGER_DOT_COLORS: Record<"dcfc" | "l2" | "hotel" | "camp", string> = {
  dcfc: "#06b6d4",
  l2: "#a3e635",
  hotel: "#a78bfa",
  camp: "#22c55e",
};

// Canvas-rendered styling for charger circleMarkers. Used instead
// of DivIcons so a long-trip corridor with 100+ chargers contributes
// zero DOM nodes (Leaflet draws all of them onto the map's canvas
// renderer). The shape mirrors the old chargerDotIcon: hotels are
// slightly larger with a white outline; other variants get a thin
// dark outline.
export function chargerCircleStyle(variant: "dcfc" | "l2" | "hotel" | "camp") {
  const fill = CHARGER_DOT_COLORS[variant];
  if (variant === "hotel" || variant === "camp") {
    return { radius: 5, fill, stroke: "#fafafa", weight: 2 };
  }
  return { radius: 4, fill, stroke: "#0a0a0a", weight: 1.5 };
}

// bookingDeepLink builds a Booking.com search URL pre-filled with
// the hotel's name + a check-in date inferred from the trip's
// departure datetime. Check-out defaults to next day. Affiliate
// ID is appended when the operator has set RIVOLT_BOOKING_AFFILIATE_ID
// so the operator captures commission on user bookings; missing
// affiliate is harmless (link still opens, just unattributed).
export function bookingDeepLink(poi: POI, departureAt: string | undefined): string {
  const name = poi.name || poi.network || "";
  if (!name) return "";
  const baseDate = departureAt ? new Date(departureAt) : new Date();
  if (isNaN(baseDate.getTime())) return "";
  const checkin = isoDate(baseDate);
  const checkout = isoDate(new Date(baseDate.getTime() + 24 * 60 * 60 * 1000));
  const url = new URL("https://www.booking.com/searchresults.html");
  // Pin the search to the POI's coordinates. Without lat/lon
  // Booking.com matches the `ss` text against its destination
  // database and prefers famous cities — searching "George's Drive
  // Inn" alone lands on a hotel in London. dest_type=latlong tells
  // Booking the latlon below IS the destination so results stay
  // near the actual POI; ss is kept as a name hint to bias toward
  // the exact property when one exists.
  url.searchParams.set("ss", name);
  url.searchParams.set("dest_type", "latlong");
  url.searchParams.set("latitude", poi.lat.toFixed(6));
  url.searchParams.set("longitude", poi.lon.toFixed(6));
  url.searchParams.set("checkin", checkin);
  url.searchParams.set("checkout", checkout);
  // group_adults=2 + no_rooms=1 = sensible Booking.com defaults
  // that match the search-bar autofill so the user lands on a
  // results page instead of a form.
  url.searchParams.set("group_adults", "2");
  url.searchParams.set("no_rooms", "1");
  const aid = bookingAffiliateID();
  if (aid) url.searchParams.set("aid", aid);
  return url.toString();
}

export function isoDate(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

// A hotel listing without a power rating is taken as a 7 kW Level 2 charger.
export const HOTEL_L2_ASSUMED_KW = 7;
export const hotelKw = (poi: POI) => (poi.maxPowerKW && poi.maxPowerKW > 0 ? poi.maxPowerKW : HOTEL_L2_ASSUMED_KW);

// campPopupHTML is the popup of a Recreation.gov campground pin. Everything from the data is escaped; the only link is the
// campground's page (built from a numeric id) and the fixed credit.
export function campPopupHTML(poi: POI, camp: { hookup: HookupId; kw: number; url: string }, overnightable: boolean): string {
  const name = poi.name || "Campground";
  const muted = "color:#a3a3a3";
  const out: string[] = [
    `<div class="charger-popup-body" style="font:12px/1.4 ui-sans-serif,system-ui;color:#fafafa;min-width:170px;max-width:240px">`,
    `<div style="font-weight:600;margin-bottom:3px">${escapeHTML(name)} <span style="font-weight:400;${muted}">(campground)</span></div>`,
    `<div>${escapeHTML(outletLabel(camp.hookup))}</div>`,
    `<div>${escapeHTML(campPowerLine(camp.hookup, camp.kw))}</div>`,
  ];
  const sites = sitesLine(poi.electricSites, poi.sites);
  if (sites) out.push(`<div style="${muted}">${escapeHTML(sites)}</div>`);
  out.push(`<div style="color:#fbbf24;margin-top:3px">${escapeHTML(CAMP_WARNING)}</div>`);
  if (camp.url) {
    out.push(`<a href="${camp.url}" target="_blank" rel="noopener noreferrer" style="display:block;margin-top:4px;color:#34d399">on Recreation.gov</a>`);
  }
  out.push(
    `<div style="${muted};font-size:${MAP_CAPTION_PX}px;margin-top:4px">${RIDB_CREDIT.lead}` +
      `<a href="${RIDB_CREDIT.source.href}" target="_blank" rel="noopener noreferrer" style="color:#a3a3a3">${escapeHTML(RIDB_CREDIT.source.label)}</a>, ` +
      `<a href="${RIDB_CREDIT.licence.href}" target="_blank" rel="noopener noreferrer" style="color:#a3a3a3">${escapeHTML(RIDB_CREDIT.licence.label)}</a>${RIDB_CREDIT.tail}</div>`,
  );
  if (overnightable) {
    out.push(
      `<button class="charger-overnight-btn" style="margin-top:6px;width:100%;padding:3px 8px;` +
        `background:#059669;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:11px">` +
        `Use as overnight stop</button>`,
    );
  }
  out.push(`</div>`);
  return out.join("");
}

export function chargerPopupHTML(
  poi: POI,
  addable: boolean,
  previewable: boolean,
  bookingURL: string,
  overnightable: boolean,
): string {
  const name = poi.name || poi.network || "Charging station";
  const out: string[] = [
    `<div class="charger-popup-body" style="font:12px/1.4 ui-sans-serif,system-ui;color:#fafafa;min-width:170px">`,
    `<div style="font-weight:600;margin-bottom:3px">${escapeHTML(name)}</div>`,
  ];
  if (bookingURL) {
    // A hotel: say what the listing says, and that it is only a listing.
    out.push(
      poi.maxPowerKW && poi.maxPowerKW > 0
        ? `<div>Listed Level 2 charger, up to ${poi.maxPowerKW.toFixed(0)} kW</div>`
        : `<div>Listed Level 2 charger, power not listed (${HOTEL_L2_ASSUMED_KW} kW assumed)</div>`,
      `<div style="color:#fbbf24;margin-top:3px">Listed, not guaranteed. Chargers can be in use, broken or guests-only; check with the hotel before you rely on it.</div>`,
    );
  } else if (poi.maxPowerKW && poi.maxPowerKW > 0) {
    out.push(`<div>Up to ${poi.maxPowerKW.toFixed(0)} kW</div>`);
  }
  if (poi.network && poi.network !== name) {
    out.push(`<div style="color:#a3a3a3">${escapeHTML(poi.network)}</div>`);
  }
  if (poi.capacity && poi.capacity > 0) {
    out.push(`<div style="color:#a3a3a3">${poi.capacity} port${poi.capacity !== 1 ? "s" : ""}</div>`);
  }
  if (bookingURL) {
    // Hotel-with-L2 row: deep-link to Booking.com's search for
    // this property + the trip's check-in date. Opens in a new
    // tab so the planner state isn't lost. No live price yet -
    // user sees rates on Booking.com (slice b adds inline prices
    // once we have an affiliate API key).
    out.push(
      `<a href="${bookingURL}" target="_blank" rel="noopener noreferrer" ` +
      `style="display:block;margin-top:6px;padding:3px 8px;background:#7c3aed;color:#fff;` +
      `border-radius:4px;font-size:11px;text-align:center;text-decoration:none">` +
      `Hotel details</a>`,
      `<div style="color:#a3a3a3;font-size:${MAP_CAPTION_PX}px;margin-top:2px;text-align:center">Opens booking.com</div>`,
    );
  }
  if (overnightable) {
    // Hotel L2: dedicated overnight-stop button. Replaces the
    // preview-detour path for hotels — pushing a hotel through the
    // single-leg replan doesn't make sense; it always ends the day.
    // Emerald (primary action / "add to my plan") rather than the
    // violet of the external Booking.com link above, so the two
    // buttons read as distinct intents.
    out.push(
      `<button class="charger-overnight-btn" style="margin-top:6px;width:100%;padding:3px 8px;` +
      `background:#059669;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:11px">` +
      `Use as overnight stop</button>`,
    );
  } else if (previewable) {
    // Preview-first path. Rivian's planner re-runs with this stop
    // added; the result names which planned stops the candidate
    // keeps, drops, or replaces.
    out.push(
      `<button class="charger-preview-btn" style="margin-top:6px;width:100%;padding:3px 8px;` +
      `background:#0e7490;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:11px">` +
      `Preview detour</button>`,
    );
  } else if (addable) {
    // Fallback (no preview wiring): direct add.
    out.push(
      `<button class="charger-add-btn" style="margin-top:6px;width:100%;padding:3px 8px;` +
      `background:#0e7490;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:11px">` +
      `Add as waypoint</button>`,
    );
  }
  out.push(`</div>`);
  return out.join("");
}

// chargerPreviewBodyHTML is the popup contents AFTER the preview
// resolves. Same outer .charger-popup-body so subsequent re-clicks
// still hit the wired buttons. Format mirrors the alternative-route
// diff line so the user reads "deltas" the same way across both
// surfaces.
export function chargerPreviewBodyHTML(poi: POI, r: PreviewResult): string {
  const name = poi.name || poi.network || "Charging station";
  const fmtMoney = (v: number) =>
    new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: r.currency || "USD",
      maximumFractionDigits: 0,
    }).format(v);
  const fmtMin = (sec: number) => `${Math.round(Math.abs(sec) / 60)} min`;
  const parts: string[] = [];
  if (Math.abs(r.delta_total_time_sec) >= 60) {
    parts.push(r.delta_total_time_sec > 0 ? `+${fmtMin(r.delta_total_time_sec)}` : `-${fmtMin(r.delta_total_time_sec)}`);
  }
  if (Math.abs(r.delta_cost) >= 0.5) {
    parts.push(r.delta_cost > 0 ? `+${fmtMoney(r.delta_cost)}` : `-${fmtMoney(Math.abs(r.delta_cost))}`);
  }
  if (Math.abs(r.delta_arrival_soc_pct) >= 1) {
    parts.push(
      r.delta_arrival_soc_pct > 0
        ? `arrives ${r.delta_arrival_soc_pct.toFixed(0)}% higher`
        : `arrives ${Math.abs(r.delta_arrival_soc_pct).toFixed(0)}% lower`,
    );
  }
  if (r.delta_stop_count !== 0) {
    parts.push(
      r.delta_stop_count > 0
        ? `+${r.delta_stop_count} stop${r.delta_stop_count === 1 ? "" : "s"}`
        : `${r.delta_stop_count} stop${r.delta_stop_count === -1 ? "" : "s"}`,
    );
  }
  // Color the delta line by net direction: time is the dominant
  // signal - faster = green, slower = amber. Tied to time so the
  // user reads at a glance whether this stop is worth taking.
  const slower = r.delta_total_time_sec > 0;
  const tone = slower ? "#fbbf24" : "#34d399";
  // Substitution + kept/dropped story. The honest version of
  // "swap": Rivian replanned around the new waypoint; here's what
  // it actually did to your original stops.
  const planChanges: string[] = [];
  if (r.substituted_for) {
    planChanges.push(`replaces ${escapeHTML(r.substituted_for)}`);
  } else if (r.stops_dropped > 0) {
    planChanges.push(`Rivian dropped ${r.stops_dropped} planned stop${r.stops_dropped === 1 ? "" : "s"}`);
  }
  if (r.stops_kept > 0) {
    planChanges.push(`keeps ${r.stops_kept} planned stop${r.stops_kept === 1 ? "" : "s"}`);
  }
  return [
    `<div style="font-weight:600;margin-bottom:3px">${escapeHTML(name)}</div>`,
    parts.length > 0
      ? `<div style="color:${tone};margin:4px 0">${parts.join(" · ")}</div>`
      : `<div style="color:#a3a3a3;margin:4px 0">No meaningful change</div>`,
    planChanges.length > 0
      ? `<div style="color:#a3a3a3;font-size:11px;margin:2px 0 4px">${planChanges.join(" · ")}</div>`
      : "",
    `<div style="display:flex;gap:4px;margin-top:6px">`,
    `<button class="charger-confirm-btn" style="flex:1;padding:3px 8px;background:#059669;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:11px">${r.substituted_for ? "Swap in" : "Add detour"}</button>`,
    `<button class="charger-cancel-btn" style="padding:3px 8px;background:transparent;color:#a3a3a3;border:1px solid #404040;border-radius:4px;cursor:pointer;font-size:11px">Cancel</button>`,
    `</div>`,
  ].join("");
}


// valhallaMultiRoute POSTs every waypoint as a single trip and
// stitches the per-leg polyline6 shapes into one polyline. Returns
// null on any failure so the caller keeps the straight-line
// placeholder.
export async function valhallaMultiRoute(
  ws: PlannedWaypoint[],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  const base = valhallaBase();
  if (base === "" || ws.length < 2) return null;
  const body = {
    locations: ws.map((w, i) => ({
      lat: w.Latitude,
      lon: w.Longitude,
      // Origin/destination are hard breaks; intermediate charging stops
      // use "through" so Valhalla doesn't fail if it can't snap a charger
      // location to the exact road network.
      type: i === 0 || i === ws.length - 1 ? "break" : "through",
    })),
    costing: "auto",
    directions_options: { units: "miles" },
  };
  try {
    const r = await fetch(`${base}/route`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
    if (!r.ok) return null;
    const j = (await r.json()) as {
      trip?: { legs?: { shape?: string }[] };
    };
    const legs = j.trip?.legs ?? [];
    if (legs.length === 0) return null;
    const out: [number, number][] = [];
    for (let i = 0; i < legs.length; i++) {
      const seg = decodePolyline6(legs[i].shape);
      if (i > 0 && seg.length > 0) seg.shift();
      out.push(...seg);
    }
    return out.length > 1 ? out : null;
  } catch {
    return null;
  }
}

// decodePolyline6 mirrors the Valhalla branch of DriveMap's
// decodePolyline. Inlined here to keep TripRouteMap self-contained;
// if a third caller appears, hoist into lib/.
export function decodePolyline6(s: string | undefined | null): [number, number][] {
  if (!s) return [];
  const factor = 1e-6;
  const out: [number, number][] = [];
  let lat = 0;
  let lon = 0;
  let i = 0;
  const n = s.length;
  while (i < n) {
    let result = 0;
    let shift = 0;
    let b: number;
    do {
      if (i >= n) return out;
      b = s.charCodeAt(i++) - 63;
      result |= (b & 0x1f) << shift;
      shift += 5;
    } while (b >= 0x20);
    const dlat = (result & 1) !== 0 ? ~(result >> 1) : result >> 1;
    lat += dlat;

    result = 0;
    shift = 0;
    do {
      if (i >= n) return out;
      b = s.charCodeAt(i++) - 63;
      result |= (b & 0x1f) << shift;
      shift += 5;
    } while (b >= 0x20);
    const dlon = (result & 1) !== 0 ? ~(result >> 1) : result >> 1;
    lon += dlon;

    out.push([lat * factor, lon * factor]);
  }
  return out;
}
