// The Leaflet maps of the charge and overview pages. (The drive detail map is MapLibre now: ../map/DriveMap.tsx.)
//
// We use Leaflet directly (not react-leaflet's MapContainer abstractions)
// to keep the lifecycle explicit and fit-bounds logic straightforward.
// The tile layer points at the public OSM tile server, which is fine for
// a personal tool but you'd swap in a self-hosted or paid provider for
// higher traffic.

import { escapeHTML } from "../lib/escapeHtml";
import { decodePolyline } from "../lib/polyline";
import { displayTimeZone } from "../lib/format";
import { useEffect, useRef } from "react";
import L from "leaflet";
import { addTilesUnavailableNotice } from "../lib/mapNotice";
import { choosePin } from "../lib/chargerNetwork";
import "leaflet/dist/leaflet.css";
import {
  valhallaBase,
  ensureConfig,
  tilesPMTilesURL,
} from "../lib/config";
import { leafletLayer, paintRules, labelRules } from "protomaps-leaflet";
import { namedFlavor, type Flavor as PMFlavor } from "@protomaps/basemaps";
import { appMapFlavor, onAppMapFlavor, type MapFlavor } from "../lib/mapTheme";
import { findNearestCharger, type POI } from "../lib/poi";
import { MAP_CAPTION_PX } from "../lib/mapCaption";
import type { SnapPoint } from "../map/snap";

// Vector attribution for the self-hosted Protomaps basemap. OSM is
// the data source; Protomaps builds and hosts the daily planet
// extracts we slice from at build time.
const PROTOMAPS_ATTRIB =
  '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · <a href="https://protomaps.com">Protomaps</a>';

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
function osmLinkURL(poi: POI): string {
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
function chargerSpecListHTML(poi: POI): string {
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

// Basemap flavor. @protomaps/basemaps ships four named flavors
// (dark / light / black / white) but black is a marginal variant of
// dark and white of light, so we expose just dark and light. The
// default is the resolved app theme (lib/mapTheme.ts), live. The D/L
// buttons are a temporary override for this map view only: held in the
// map's closure, never persisted, so the next map opens in the theme.
type Flavor = MapFlavor;
const FLAVORS: Flavor[] = ["dark", "light"];

// flavorControl renders a two-button picker in the top-right corner.
// `pick` is called on click; the returned `refresh` re-highlights the
// active flavor.
function flavorControl(
  map: L.Map,
  active: Flavor,
  pick: (f: Flavor) => void,
): { refresh: (f: Flavor) => void } {
  const buttons = {} as Record<Flavor, HTMLButtonElement>;
  const refresh = (cur: Flavor) => {
    for (const f of FLAVORS) {
      const b = buttons[f];
      if (!b) continue;
      if (f === cur) {
        b.style.background = "#262626";
        b.style.color = "#fafafa";
        b.style.borderColor = "#404040";
      } else {
        b.style.background = "transparent";
        b.style.color = "#a3a3a3";
        b.style.borderColor = "transparent";
      }
    }
  };
  const ctl = new L.Control({ position: "topright" });
  ctl.onAdd = () => {
    const div = L.DomUtil.create("div", "rivolt-flavor-control");
    div.style.cssText =
      "background:rgba(10,10,10,0.78);border:1px solid #262626;" +
      "border-radius:6px;padding:2px;display:inline-flex;gap:2px;" +
      `font:${MAP_CAPTION_PX}px/1 ui-sans-serif,system-ui;backdrop-filter:blur(2px);` +
      "box-shadow:0 1px 2px rgba(0,0,0,0.4);";
    for (const f of FLAVORS) {
      const btn = L.DomUtil.create("button", "", div) as HTMLButtonElement;
      btn.type = "button";
      btn.textContent = f[0].toUpperCase();
      btn.title = `${f[0].toUpperCase()}${f.slice(1)} basemap`;
      btn.style.cssText =
        "width:20px;height:20px;border:1px solid transparent;" +
        "border-radius:4px;background:transparent;color:#a3a3a3;" +
        "cursor:pointer;padding:0;font:inherit;font-weight:600;";
      L.DomEvent.on(btn, "click", (e) => {
        L.DomEvent.stop(e);
        pick(f);
      });
      buttons[f] = btn;
    }
    refresh(active);
    L.DomEvent.disableClickPropagation(div);
    L.DomEvent.disableScrollPropagation(div);
    return div;
  };
  ctl.addTo(map);
  return { refresh };
}

// boostContrast returns a copy of a named protomaps flavor with
// building / POI / landuse colors lifted so they actually read
// against the earth color at street zoom. The shipped named
// flavors are tuned for "minimal" cartography -- DARK puts
// buildings at #111 over an earth of #1f1f1f at 50% opacity,
// which composites to roughly the same gray and erases building
// footprints visually. Same story in LIGHT (buildings = #ccc on
// earth = #e2dfda) and BLACK / WHITE.
//
// We only override fields where the shipped value visually
// merges with earth; other fields (roads, water, labels) are
// left untouched so the overall theme still feels right.
function boostContrast(name: Flavor, base: PMFlavor): PMFlavor {
  switch (name) {
    case "dark":
      return {
        ...base,
        // Buildings need to be clearly above earth (#1f1f1f).
        // #555 at 0.5 opacity composites to ~#3a -- visible but
        // not loud.
        buildings: "#555555",
        // Parks / wood / pedestrian areas: lift the green tint
        // so a city park reads as "this is a park," not "this
        // is slightly different gray."
        park_a: "#1d3527",
        park_b: "#1d3527",
        wood_a: "#1d2d24",
        wood_b: "#1d2d24",
        pedestrian: "#2a2a2a",
        // Industrial / commercial / school landuse: subtle
        // warm-gray so big-box parking lots stop looking like
        // empty earth.
        industrial: "#2c2a26",
        school: "#2c2828",
        hospital: "#2c2828",
      };
    case "light":
      return {
        ...base,
        // LIGHT ships buildings = #cccccc over earth = #e2dfda
        // at 0.5 opacity -- fully invisible. Drop to a clearly
        // darker tone.
        buildings: "#a8a8a8",
        park_a: "#cfe5cf",
        park_b: "#cfe5cf",
        pedestrian: "#dad7d2",
      };
  }
}

// addBasemap draws the self-hosted PMTiles vector layer when the tile
// server is wired via /api/config; otherwise it shows a "tiles
// unavailable" notice on a plain background (no third-party raster
// host). The vector path renders labels inline with the basemap; the
// route polyline draws on top.
//
// When the vector path is active, the layer is swapped when the app
// theme flips or the D/L override is used. Observers are removed on map
// unload.
function addBasemap(map: L.Map) {
  const url = tilesPMTilesURL();
  if (!url) {
    addTilesUnavailableNotice(map);
    return;
  }
  // protomaps-leaflet's leafletLayer returns its own LeafletLayer
  // class which extends L.GridLayer at runtime but isn't typed as
  // a Leaflet Layer in the published .d.ts. Cast at the boundary.
  //
  // We don't pass `flavor: f` directly: protomaps-leaflet's named
  // flavors (especially `dark` and `light`) ship with building /
  // POI / landuse colors so close to the earth color that
  // buildings effectively disappear at street zoom. We compute
  // paintRules/labelRules ourselves from a contrast-boosted copy
  // of the named flavor so building footprints, parks, and
  // service-area landuse all read clearly at z16+.
  const mkLayer = (f: Flavor) => {
    const base = namedFlavor(f);
    const boosted = boostContrast(f, base);
    return leafletLayer({
      url,
      paintRules: paintRules(boosted),
      labelRules: labelRules(boosted, "en"),
      backgroundColor: boosted.background,
      attribution: PROTOMAPS_ATTRIB,
    }) as unknown as L.Layer;
  };
  // The container's own background shows until tiles paint; leaflet.css
  // (unlayered) beats our themed rule, so set it per flavor here.
  const paintBg = (f: Flavor) => {
    map.getContainer().style.background = boostContrast(f, namedFlavor(f)).background;
  };
  let shown: Flavor = appMapFlavor();
  let current = mkLayer(shown);
  current.addTo(map);
  paintBg(shown);
  const show = (next: Flavor) => {
    if (next === shown) return;
    shown = next;
    paintBg(next);
    const replacement = mkLayer(next);
    replacement.addTo(map);
    map.removeLayer(current);
    current = replacement;
  };
  const ctl = flavorControl(map, shown, (f) => {
    show(f);
    ctl.refresh(f);
  });
  // A theme change (Settings, or the OS under System) resets the
  // temporary override: the map goes back to following the app.
  const off = onAppMapFlavor((f) => {
    show(f);
    ctl.refresh(f);
  });
  map.once("unload", off);
}

// Leaflet ships broken marker icon URLs when bundled. Replace them with
// an inline DOM marker so we don't need bundler asset plumbing.
// Emerald for start, rose for end, amber for charge. A thin dark ring
// keeps the dot legible against both the polyline and the basemap.
function circleIcon(color: string): L.DivIcon {
  return L.divIcon({
    className: "rivolt-map-marker",
    html: `<span style="display:block;width:14px;height:14px;border-radius:9999px;background:${color};border:2px solid #0a0a0a;box-shadow:0 0 0 2px ${color}33;"></span>`,
    iconSize: [14, 14],
    iconAnchor: [7, 7],
  });
}

// ChargeMap is a single-pin variant for charge sessions.
//
// No GPS-staleness badge here. The badge on DriveMap warns that a
// moving polyline may be miles off from where the vehicle actually
// drove; on a parked car the recorded charge marker is fine even if
// the modem stopped emitting fresh fixes during the session — and
// parked vehicles produce stale fixes routinely (no movement → the
// modem stops urgently re-fixing). The badge fired on most charges
// with zero actionable signal, so we removed the prop entirely.
export function ChargeMap({
  lat,
  lon,
  height = 240,
  network,
  dc = false,
  recorded = false,
  ownLabel,
}: {
  // #906: the user said the charge happened where the truck was: the
  // recorded position is the pin, never a snap to a public charger.
  recorded?: boolean;
  // #906: the name of one of the user's own networks whose saved
  // location is near this charge: the recorded position is labelled
  // with it, never snapped.
  ownLabel?: string | null;
  // The network the ledger recorded for this charge (RAN, Electrify
  // America, ...). When the snapped charger node is a different
  // network's, the pin says this one instead of a wrong brand.
  network?: string | null;
  // The session was DC fast charging (peak power); only then may an
  // unlabelled pin say "DC fast charger".
  dc?: boolean;
  lat: number;
  lon: number;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  // The map is built once per location. A late `network` (the ledger
  // arrives after the page) only re-runs the pin choice through this ref,
  // so the map isn't torn down and rebuilt (that flashed).
  const pinInputs = useRef({ network, dc, recorded, ownLabel });
  pinInputs.current = { network, dc, recorded, ownLabel };
  const rePin = useRef<(() => void) | null>(null);
  useEffect(() => {
    rePin.current?.();
  }, [network, dc, recorded, ownLabel]);

  useEffect(() => {
    if (!ref.current) return;
    if (!Number.isFinite(lat) || !Number.isFinite(lon) || (lat === 0 && lon === 0)) {
      return;
    }
    const map = L.map(ref.current, {
      zoomControl: true,
      preferCanvas: true,
      scrollWheelZoom: false,
      zoomSnap: 0.25,
      zoomDelta: 0.5,
      wheelPxPerZoomLevel: 120,
      fadeAnimation: true,
    }).setView([lat, lon], 16);
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addBasemap(map);

    // Layered marker model:
    //   - Always show the recorded GPS position (where the vehicle
    //     actually pinged from). Starts as the prominent amber pin.
    //   - Async query the basemap pmtiles for the nearest OSM
    //     charging_station POI within 250 m. If found, demote the
    //     recorded-GPS pin to a small muted dot, draw a dashed
    //     connector, and paint a labeled charger pin at the OSM
    //     coords. The label is the OSM `name` tag.
    //
    // We can't determine "which physical charger" deterministically
    // from a single GPS sample (parking lots routinely host several
    // stations within 50 m of each other), so we pick the closest
    // and show the user the snap so they can sanity-check it.
    const recordedMarker = L.marker([lat, lon], {
      icon: circleIcon("#f59e0b"),
      zIndexOffset: 1000,
    })
      .addTo(map)
      .bindTooltip("Charge location", { direction: "top" });

    let chargerMarker: L.Marker | null = null;
    let connector: L.Polyline | null = null;
    let cancelled = false;

    // Attempt the snap unconditionally — findNearestCharger awaits
    // /api/config internally and exits cleanly if neither archive is
    // wired. 750m radius covers the typical "GPS landed at
    // parking-lot entrance, OSM marker is in the middle of the lot"
    // geometry; 250m was too tight for big-box and travel-plaza
    // stations.
    let snapped: POI | null | undefined; // undefined until the lookup settles
    const choose = () => {
      const own = (pinInputs.current.ownLabel ?? "").trim();
      const userPinned = pinInputs.current.recorded || own !== "";
      if (cancelled || (snapped === undefined && !userPinned)) return;
      const known = (pinInputs.current.network ?? "").trim();
      // Reset to the plain recorded-location pin, then apply the choice.
      if (chargerMarker) {
        chargerMarker.remove();
        chargerMarker = null;
      }
      if (connector) {
        connector.remove();
        connector = null;
      }
      recordedMarker.setIcon(circleIcon("#f59e0b"));
      recordedMarker.setZIndexOffset(1000);
      recordedMarker.unbindTooltip();
      recordedMarker.bindTooltip("Charge location", { direction: "top" });
      // The user's own word beats any snap: their network's name on
      // the recorded position, or the plain recorded pin.
      if (own) {
        labelOwnLocation(own);
        return;
      }
      if (pinInputs.current.recorded) {
        recordedMarker.unbindTooltip();
        recordedMarker.bindTooltip("Where the truck was", { direction: "top" });
        return;
      }
      if (!snapped && !known) return;
      // Never a wrong brand: the snap is kept only when the snapped
      // node agrees with the ledger's network (or the network is
      // unknown); otherwise the charge's own location is labelled with
      // the known network.
      const choice = choosePin(snapped ?? null, known, pinInputs.current.dc);
      if (choice.snap && snapped) applyChargerSnap(snapped, choice.label);
      else labelOwnLocation(choice.label);
    };
    rePin.current = choose;
    choose(); // a pin the user chose needs no lookup
    void findNearestCharger(lat, lon, 750).then((poi) => {
      snapped = poi ?? null;
      choose();
    });

    function chargerPinIcon(): L.DivIcon {
      return L.divIcon({
        className: "rivolt-charger-pin",
        html:
          '<span style="display:inline-flex;align-items:center;justify-content:center;' +
          "width:22px;height:22px;border-radius:6px;background:#f59e0b;" +
          'border:2px solid #0a0a0a;box-shadow:0 0 0 2px #f59e0b33;' +
          'color:#0a0a0a;font:700 13px/1 ui-sans-serif,system-ui;">⚡</span>',
        iconSize: [22, 22],
        iconAnchor: [11, 11],
      });
    }

    // labelOwnLocation: no trustworthy snap, so the recorded location
    // itself becomes the labelled charger pin.
    function labelOwnLocation(label: string) {
      recordedMarker.setIcon(chargerPinIcon());
      recordedMarker.unbindTooltip();
      recordedMarker.bindTooltip(escapeHTML(label), {
        direction: "top",
        permanent: true,
        // Clear of the 22 px pin: the bubble sits above it, never on it.
        offset: [0, -16],
        className: "rivolt-charger-label",
      });
    }

    function applyChargerSnap(poi: POI, label?: string) {
      // Demote recorded-GPS to a muted dot with a clarifying
      // tooltip. Keeping it visible (rather than removing it)
      // surfaces the snap distance for the user.
      recordedMarker.setIcon(
        L.divIcon({
          className: "rivolt-map-recorded-gps",
          html:
            '<span style="display:block;width:8px;height:8px;border-radius:9999px;' +
            'background:#737373;border:1.5px solid #0a0a0a;opacity:0.85;"></span>',
          iconSize: [8, 8],
          iconAnchor: [4, 4],
        }),
      );
      recordedMarker.setZIndexOffset(500);
      recordedMarker.unbindTooltip();
      recordedMarker.bindTooltip(
        `Recorded GPS · ${Math.round(poi.distanceM)} m from charger`,
        { direction: "top" },
      );

      // Dashed connector from recorded GPS to the snapped charger
      // so the user immediately sees the relationship between the
      // two pins.
      connector = L.polyline(
        [
          [lat, lon],
          [poi.lat, poi.lon],
        ],
        {
          color: "#f59e0b",
          weight: 1.5,
          opacity: 0.6,
          dashArray: "4 4",
          interactive: false,
        },
      ).addTo(map);

      // The snapped charger pin: amber bolt-styled square with the
      // OSM name as a permanent label so the charger identifies
      // itself without requiring a click.
      const labelText = label ?? poi.name ?? "Charging station";
      chargerMarker = L.marker([poi.lat, poi.lon], {
        icon: L.divIcon({
          className: "rivolt-charger-pin",
          html:
            '<span style="display:inline-flex;align-items:center;justify-content:center;' +
            "width:22px;height:22px;border-radius:6px;background:#f59e0b;" +
            'border:2px solid #0a0a0a;box-shadow:0 0 0 2px #f59e0b33;' +
            'color:#0a0a0a;font:700 13px/1 ui-sans-serif,system-ui;">⚡</span>',
          iconSize: [22, 22],
          iconAnchor: [11, 11],
        }),
        zIndexOffset: 2000,
      })
        .addTo(map)
        .bindTooltip(escapeHTML(labelText), {
          direction: "top",
          permanent: true,
          offset: [0, -16],
          className: "rivolt-charger-label",
        })
        .bindPopup(
          '<div style="font:12px/1.4 ui-sans-serif,system-ui;color:#fafafa;min-width:180px;max-width:260px">' +
            `<div style="font-weight:600;margin-bottom:2px">${escapeHTML(labelText)}</div>` +
            chargerSpecListHTML(poi) +
            '<div style="color:#a3a3a3;font-size:11px;margin-top:4px">' +
            `Snapped from <a href="${osmLinkURL(poi)}" target="_blank" rel="noopener" style="color:#f59e0b;text-decoration:underline">OpenStreetMap</a> · ${Math.round(poi.distanceM)} m from recorded GPS` +
            "</div></div>",
        );

      // Re-center on the snapped charger if it falls within the
      // current viewport bounds; otherwise leave the view alone so
      // we don't pan unexpectedly far when the snap distance is
      // close to the radius limit.
      if (map.getBounds().contains([poi.lat, poi.lon])) {
        map.panTo([poi.lat, poi.lon], { animate: true });
      }
    }

    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      cancelled = true;
      rePin.current = null;
      cancelAnimationFrame(rAF);
      ro.disconnect();
      if (chargerMarker) chargerMarker.remove();
      if (connector) connector.remove();
      map.remove();
    };
  }, [lat, lon]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}

// ChargeBucket categorizes a session for the overview map by power.
// "fast" is the server's DC fast charge (kind, the ledger's rule, as on
// Money and every other page); only without a classification does it
// fall back to peak power.
//   - "fast": kind dcfc (else ≥ 50 kW peak)
//   - "l2":   3 kW peak or more, AC (240 V, public or home)
//   - "l1":   < 3 kW peak (110 V trickle / unknown)
function chargeBucket(c: { MaxPowerKW: number; kind?: "ac" | "dcfc" }): "fast" | "l2" | "l1" {
  if (c.kind === "dcfc") return "fast";
  const maxKW = c.MaxPowerKW;
  if (c.kind === undefined && Number.isFinite(maxKW) && maxKW >= 50) return "fast";
  if (!Number.isFinite(maxKW) || maxKW < 3) return "l1";
  return "l2";
}

const CHARGE_BUCKET_COLOR: Record<"fast" | "l2" | "l1", string> = {
  fast: "#f43f5e",
  l2: "#06b6d4",
  l1: "#a3a3a3",
};

// dotIcon is a smaller no-ring marker for overview maps where many
// pins sit in close proximity (e.g. dozens of home charges within
// 10 m). Larger ringed icons collapse into a single ambiguous blob.
function dotIcon(color: string): L.DivIcon {
  return L.divIcon({
    className: "rivolt-map-dot",
    html: `<span style="display:block;width:10px;height:10px;border-radius:9999px;background:${color};border:1.5px solid #0a0a0a;box-shadow:0 0 0 1px ${color}66;"></span>`,
    iconSize: [10, 10],
    iconAnchor: [5, 5],
  });
}

// ChargesOverviewMap renders one pin per charge, colored by power
// bucket, with a tooltip summarizing the session. Clicking a pin
// invokes onSelect (typically used to navigate to the detail page).
// Pins at the same physical spot (home charging) stack — Leaflet's
// canvas renderer handles the overlap fine; we don't cluster.
export function ChargesOverviewMap({
  charges,
  onSelect,
  height = 360,
}: {
  charges: {
    ID: string;
    Lat: number;
    Lon: number;
    StartedAt: string;
    EnergyAddedKWh: number;
    MaxPowerKW: number;
  }[];
  onSelect?: (id: string) => void;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  // Stash the latest onSelect so click handlers don't go stale across
  // renders (the map only mounts once per visible-charge set).
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;

  // Build a stable signature so we only rebuild the map when the
  // visible set actually changes (not on every parent re-render).
  const sig = charges
    .map((c) => `${c.ID}:${c.Lat.toFixed(5)},${c.Lon.toFixed(5)}`)
    .join("|");

  useEffect(() => {
    if (!ref.current) return;
    const valid = charges.filter(
      (c) =>
        Number.isFinite(c.Lat) &&
        Number.isFinite(c.Lon) &&
        (c.Lat !== 0 || c.Lon !== 0),
    );
    if (valid.length === 0) return;

    const map = L.map(ref.current, {
      zoomControl: true,
      preferCanvas: true,
      scrollWheelZoom: false,
      zoomSnap: 0.25,
      zoomDelta: 0.5,
      wheelPxPerZoomLevel: 120,
      fadeAnimation: true,
    }).setView([valid[0].Lat, valid[0].Lon], 4);
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addBasemap(map);

    for (const c of valid) {
      const bucket = chargeBucket(c);
      const m = L.marker([c.Lat, c.Lon], {
        icon: dotIcon(CHARGE_BUCKET_COLOR[bucket]),
      }).addTo(map);
      const when = new Date(c.StartedAt).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
        timeZone: displayTimeZone(),
      });
      m.bindTooltip(
        `<div style="font:11px/1.3 ui-sans-serif,system-ui">` +
          `<div style="color:${CHARGE_BUCKET_COLOR[bucket]};font-weight:600">` +
          `${bucket === "fast" ? "Fast charging" : bucket === "l2" ? "Level 2" : "Household outlet (Level 1)"} · ${c.MaxPowerKW.toFixed(1)} kW</div>` +
          `<div style="color:#a3a3a3">${when}</div>` +
          `<div style="color:#d4d4d4">${c.EnergyAddedKWh.toFixed(1)} kWh added</div>` +
          `</div>`,
        { direction: "top" },
      );
      m.on("click", () => onSelectRef.current?.(c.ID));
    }
    map.fitBounds(
      L.latLngBounds(valid.map((c) => [c.Lat, c.Lon] as [number, number])),
      { padding: [24, 24], maxZoom: 14 },
    );

    const legend = new L.Control({ position: "bottomleft" });
    legend.onAdd = () => {
      const div = L.DomUtil.create("div", "rivolt-charge-legend");
      div.style.cssText =
        "background:rgba(10,10,10,0.78);border:1px solid #262626;" +
        "border-radius:6px;padding:4px 6px;display:flex;gap:8px;" +
        `align-items:center;font:${MAP_CAPTION_PX}px/1.1 ui-sans-serif,system-ui;` +
        "color:#d4d4d4;backdrop-filter:blur(2px);";
      div.innerHTML = (
        [
          ["fast", "Fast charging"],
          ["l2", "Level 2"],
          ["l1", "Household outlet (Level 1)"],
        ] as const
      )
        .map(
          ([k, label]) =>
            `<span style="display:inline-flex;align-items:center;gap:3px">` +
            `<span style="display:inline-block;width:8px;height:8px;border-radius:9999px;background:${CHARGE_BUCKET_COLOR[k]}"></span>` +
            `${label}</span>`,
        )
        .join("");
      L.DomEvent.disableClickPropagation(div);
      return div;
    };
    legend.addTo(map);

    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      cancelAnimationFrame(rAF);
      ro.disconnect();
      legend.remove();
      map.remove();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}

// routeOverview fetches a road-snapped start→end geometry for drives
// that don't have a stored RoutePolyline. Lightweight: only two
// coordinates, no GPS trace required. Returns null when Valhalla
// isn't wired or the call fails, so the caller can keep the
// straight-line fallback.
async function routeOverview(
  startLat: number,
  startLon: number,
  endLat: number,
  endLon: number,
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  await ensureConfig();
  if (signal.aborted) return null;
  if (valhallaBase() === "") return null;
  const pts: SnapPoint[] = [
    { lat: startLat, lon: startLon },
    { lat: endLat, lon: endLon },
  ];
  return await valhallaRoute(pts, signal);
}

// valhallaRoute is the cheapest-path equivalent of routeAll, used by
// the multi-drive overview map. POST /route with two break points and
// decode the polyline6 shape.
async function valhallaRoute(
  pts: SnapPoint[],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  const base = valhallaBase();
  if (base === "" || pts.length < 2) return null;
  const body = {
    locations: pts.map((p) => ({ lat: p.lat, lon: p.lon })),
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
      const seg = decodePolyline(legs[i].shape, 6);
      if (i > 0 && seg.length > 0) seg.shift();
      out.push(...seg);
    }
    return out.length > 1 ? out : null;
  } catch {
    return null;
  }
}

// DrivesOverviewMap renders every drive's route on a single map.
// Drives with a stored RoutePolyline (live recorder) show the real
// GPS trace. Legacy drives (ElectraFi imports) fetch a road-snapped
// geometry from Valhalla using just the start/end coordinates.
const ROUTE_CONCURRENCY = 20;

export function DrivesOverviewMap({
  drives,
  onSelect,
  height = 360,
}: {
  drives: {
    ID: string;
    StartLat: number;
    StartLon: number;
    EndLat: number;
    EndLon: number;
    StartedAt: string;
    DistanceMi: number;
    RoutePolyline?: string;
  }[];
  onSelect?: (id: string) => void;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const sig = drives.map((d) => d.ID).join("|");

  useEffect(() => {
    if (!ref.current) return;
    const valid = drives.filter(
      (d) =>
        Number.isFinite(d.StartLat) &&
        Number.isFinite(d.StartLon) &&
        Number.isFinite(d.EndLat) &&
        Number.isFinite(d.EndLon) &&
        (d.StartLat !== 0 || d.StartLon !== 0) &&
        (d.EndLat !== 0 || d.EndLon !== 0),
    );
    if (valid.length === 0) return;

    const map = L.map(ref.current, {
      zoomControl: true,
      preferCanvas: true,
      scrollWheelZoom: false,
      zoomSnap: 0.25,
      zoomDelta: 0.5,
      wheelPxPerZoomLevel: 120,
      fadeAnimation: true,
    }).setView([valid[0].StartLat, valid[0].StartLon], 4);
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addBasemap(map);

    const ac = new AbortController();
    const allLatLngs: [number, number][] = [];

    // Render initial polylines (stored trace or straight-line
    // placeholder) and collect drives that need a Valhalla /route fetch.
    const needsRoute: {
      d: (typeof valid)[0];
      line: L.Polyline;
    }[] = [];

    for (const d of valid) {
      const start: [number, number] = [d.StartLat, d.StartLon];
      const end: [number, number] = [d.EndLat, d.EndLon];
      const trace = decodePolyline(d.RoutePolyline);
      const path: [number, number][] = trace.length >= 2 ? trace : [start, end];
      for (const p of path) allLatLngs.push(p);
      const when = new Date(d.StartedAt).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
        timeZone: displayTimeZone(),
      });
      const tooltip =
        `<div style="font:11px/1.3 ui-sans-serif,system-ui">` +
        `<div style="color:#34d399;font-weight:600">${d.DistanceMi.toFixed(1)} mi</div>` +
        `<div style="color:#a3a3a3">${when}</div>` +
        `</div>`;
      const line = L.polyline(path, {
        color: "#34d399",
        weight: 1.5,
        opacity: 0.5,
      })
        .addTo(map)
        .bindTooltip(tooltip, { sticky: true });
      line.on("click", () => onSelectRef.current?.(d.ID));
      L.marker(start, { icon: dotIcon("#10b981") })
        .addTo(map)
        .on("click", () => onSelectRef.current?.(d.ID));
      L.marker(end, { icon: dotIcon("#f43f5e") })
        .addTo(map)
        .on("click", () => onSelectRef.current?.(d.ID));

      // Queue a Valhalla /route fetch only for drives without a stored polyline.
      if (trace.length < 2) {
        needsRoute.push({ d, line });
      }
    }

    map.fitBounds(L.latLngBounds(allLatLngs), {
      padding: [24, 24],
      maxZoom: 14,
    });

    // Async pass: replace straight-line placeholders with road-snapped
    // routes, ROUTE_CONCURRENCY at a time.
    if (needsRoute.length > 0) {
      const queue = [...needsRoute];
      const worker = async () => {
        while (queue.length > 0 && !ac.signal.aborted) {
          const item = queue.shift()!;
          const routed = await routeOverview(
            item.d.StartLat,
            item.d.StartLon,
            item.d.EndLat,
            item.d.EndLon,
            ac.signal,
          );
          if (routed && routed.length >= 2 && !ac.signal.aborted) {
            item.line.setLatLngs(routed);
          }
        }
      };
      const workers = Array.from(
        { length: Math.min(ROUTE_CONCURRENCY, needsRoute.length) },
        worker,
      );
      void Promise.all(workers);
    }

    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      ac.abort();
      cancelAnimationFrame(rAF);
      ro.disconnect();
      map.remove();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}
