// ChargeMap is the single-pin map of one charge session, on the self-hosted MapLibre basemap. The recorded GPS position is always shown;
// the basemap's nearest OSM charging station within 750 m is looked up, and when it agrees with the ledger's network the position is demoted
// to a small dot with a dashed link to a labelled charger pin (click for its details). Without WebGL it says the map is unavailable.
//
// No GPS-staleness badge here: it warns that a moving polyline may be miles off, and a parked car's last fix is fine even when the modem
// stops re-fixing (it fired on most charges with nothing to act on).
import { useEffect, useMemo, useRef } from "react";
import type { GeoJSONSource, Marker, Popup } from "maplibre-gl";
import { choosePin } from "../lib/chargerNetwork";
import { ensureConfig } from "../lib/config";
import { escapeHTML } from "../lib/escapeHtml";
import { findNearestCharger, type POI } from "../lib/poi";
import { chargerSpecListHTML, osmLinkURL } from "./chargerPopup";
import { loadMapLibre } from "./loader";
import { mountBaseMap } from "./mount";
import { pinElement } from "./controls";
import { MapUnavailable } from "./Unavailable";
import { webglAvailable } from "./webgl";

const CONNECTOR = "rivolt-charge-link";
const BOLT_PIN =
  '<span style="display:inline-flex;align-items:center;justify-content:center;width:22px;height:22px;border-radius:6px;background:#f59e0b;' +
  'border:2px solid #0a0a0a;box-shadow:0 0 0 2px #f59e0b33;color:#0a0a0a;font:700 13px/1 ui-sans-serif,system-ui;">⚡</span>';
const RECORDED_DOT =
  '<span style="display:block;width:8px;height:8px;border-radius:9999px;background:#737373;border:1.5px solid #0a0a0a;opacity:0.85;"></span>';

export function ChargeMap({
  lat,
  lon,
  height = 240,
  network,
  dc = false,
  recorded = false,
  ownLabel,
}: {
  // #906: the user said the charge happened where the truck was: the recorded position is the pin, never a snap to a public charger.
  recorded?: boolean;
  // #906: the name of one of the user's own networks whose saved location is near this charge: the recorded position is labelled with it.
  ownLabel?: string | null;
  // The network the ledger recorded for this charge. When the snapped charger is another network's, the pin says this one.
  network?: string | null;
  // The session was DC fast charging (peak power); only then may an unlabelled pin say "DC fast charger".
  dc?: boolean;
  lat: number;
  lon: number;
  height?: number;
}) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  // The map is built once per location. A late `network` (the ledger arrives after the page) only re-runs the pin choice through this ref,
  // so the map is not torn down and rebuilt (that flashed).
  const pinInputs = useRef({ network, dc, recorded, ownLabel });
  pinInputs.current = { network, dc, recorded, ownLabel };
  const rePin = useRef<(() => void) | null>(null);
  useEffect(() => {
    rePin.current?.();
  }, [network, dc, recorded, ownLabel]);

  useEffect(() => {
    if (!supported || !ref.current) return;
    if (!Number.isFinite(lat) || !Number.isFinite(lon) || (lat === 0 && lon === 0)) return;
    const el = ref.current;
    let alive = true;
    const cleanups: (() => void)[] = [() => { alive = false; }];

    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !el.isConnected) return;
      const m = mountBaseMap(ml, el, { center: [lon, lat], zoom: 16 });
      const { map } = m;
      cleanups.push(() => { rePin.current = null; m.dispose(); });

      // The dashed link from the recorded position to the snapped charger, a line layer re-added after a theme change.
      let link: [number, number][] | null = null;
      const linkData = () => ({
        type: "FeatureCollection" as const,
        features: link ? [{ type: "Feature" as const, geometry: { type: "LineString" as const, coordinates: link }, properties: {} }] : [],
      });
      m.onStyle(() => {
        if (!map.getSource(CONNECTOR)) map.addSource(CONNECTOR, { type: "geojson", data: linkData() as never });
        if (!map.getLayer(CONNECTOR)) {
          map.addLayer({ id: CONNECTOR, type: "line", source: CONNECTOR, paint: { "line-color": "#f59e0b", "line-width": 1.5, "line-opacity": 0.6, "line-dasharray": [4, 4] } });
        }
      });
      const setLink = (pts: [number, number][] | null) => {
        link = pts;
        (map.getSource(CONNECTOR) as GeoJSONSource | undefined)?.setData(linkData() as never);
      };

      // The recorded position: a marker whose look and hover text change with the pin choice.
      const recordedEl = document.createElement("div");
      recordedEl.style.cssText = "display:flex;align-items:center;justify-content:center;cursor:default;";
      const setRecordedLook = (look: "pin" | "bolt" | "dot") => {
        if (look === "pin") recordedEl.replaceChildren(pinElement("#f59e0b", ""));
        else recordedEl.innerHTML = look === "bolt" ? BOLT_PIN : RECORDED_DOT;
      };
      setRecordedLook("pin");
      let recordedTip = "Charge location";
      const hover = new ml.Popup({ closeButton: false, closeOnClick: false, offset: 14, className: "rivolt-tip", maxWidth: "260px" });
      recordedEl.addEventListener("mouseenter", () => hover.setLngLat([lon, lat]).setText(recordedTip).addTo(map));
      recordedEl.addEventListener("mouseleave", () => hover.remove());
      const recordedMarker = new ml.Marker({ element: recordedEl }).setLngLat([lon, lat]).addTo(map);
      recordedEl.style.zIndex = "1000";
      cleanups.push(() => { hover.remove(); recordedMarker.remove(); });

      let chargerMarker: Marker | null = null;
      let chargerLabel: Popup | null = null;
      let ownLabelPopup: Popup | null = null;
      const label = (text: string, at: [number, number], above: number) =>
        new ml.Popup({ closeButton: false, closeOnClick: false, anchor: "bottom", offset: [0, -above], className: "rivolt-charger-label", maxWidth: "260px" })
          .setLngLat(at)
          .setHTML(escapeHTML(text))
          .addTo(map);
      const clearChoice = () => {
        chargerMarker?.remove();
        chargerMarker = null;
        chargerLabel?.remove();
        chargerLabel = null;
        ownLabelPopup?.remove();
        ownLabelPopup = null;
        setLink(null);
        setRecordedLook("pin");
        recordedEl.style.zIndex = "1000";
        recordedTip = "Charge location";
      };
      cleanups.push(() => { chargerMarker?.remove(); chargerLabel?.remove(); ownLabelPopup?.remove(); });

      // labelOwnLocation: no trustworthy snap, so the recorded location itself becomes the labelled charger pin.
      const labelOwnLocation = (text: string) => {
        setRecordedLook("bolt");
        recordedTip = "";
        hover.remove();
        ownLabelPopup = label(text, [lon, lat], 16);
      };

      const applyChargerSnap = (poi: POI, text?: string) => {
        // Demote the recorded GPS to a muted dot; keeping it visible shows the snap distance.
        setRecordedLook("dot");
        recordedEl.style.zIndex = "500";
        recordedTip = `Recorded GPS · ${Math.round(poi.distanceM)} m from charger`;
        setLink([[lon, lat], [poi.lon, poi.lat]]);
        const labelText = text ?? poi.name ?? "Charging station";
        const pinEl = document.createElement("div");
        pinEl.innerHTML = BOLT_PIN;
        pinEl.style.cssText = "display:flex;align-items:center;justify-content:center;cursor:pointer;";
        const popup = new ml.Popup({ offset: 16, maxWidth: "280px", className: "rivolt-popup" }).setHTML(
          '<div style="font:12px/1.4 ui-sans-serif,system-ui;color:#fafafa;min-width:180px;max-width:260px">' +
            `<div style="font-weight:600;margin-bottom:2px">${escapeHTML(labelText)}</div>` +
            chargerSpecListHTML(poi) +
            '<div style="color:#a3a3a3;font-size:11px;margin-top:4px">' +
            `Snapped from <a href="${osmLinkURL(poi)}" target="_blank" rel="noopener" style="color:#f59e0b;text-decoration:underline">OpenStreetMap</a> · ${Math.round(poi.distanceM)} m from recorded GPS` +
            "</div></div>",
        );
        chargerMarker = new ml.Marker({ element: pinEl }).setLngLat([poi.lon, poi.lat]).setPopup(popup).addTo(map);
        pinEl.style.zIndex = "2000";
        chargerLabel = label(labelText, [poi.lon, poi.lat], 16);
        // Re-centre on the snapped charger if it is within the current view; otherwise leave the view alone, so a snap near the radius limit
        // does not pan far.
        if (map.getBounds().contains([poi.lon, poi.lat])) map.easeTo({ center: [poi.lon, poi.lat] });
      };

      // Attempt the snap unconditionally: findNearestCharger awaits /api/config and exits cleanly when neither archive is wired. 750 m covers
      // "GPS landed at the lot entrance, the OSM marker is mid-lot"; 250 m was too tight for big-box and travel-plaza stations.
      let snapped: POI | null | undefined; // undefined until the lookup settles
      const choose = () => {
        const own = (pinInputs.current.ownLabel ?? "").trim();
        const userPinned = pinInputs.current.recorded || own !== "";
        if (!alive || (snapped === undefined && !userPinned)) return;
        const known = (pinInputs.current.network ?? "").trim();
        clearChoice();
        // The user's own word beats any snap: their network's name on the recorded position, or the plain recorded pin.
        if (own) return labelOwnLocation(own);
        if (pinInputs.current.recorded) {
          recordedTip = "Where the truck was";
          return;
        }
        if (!snapped && !known) return;
        // Never a wrong brand: the snap is kept only when the snapped node agrees with the ledger's network (or the network is unknown);
        // otherwise the charge's own location is labelled with the known network.
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
    })().catch((err) => console.warn("map failed to start:", err instanceof Error ? err.message : err));
    return () => { for (const c of cleanups.reverse()) c(); };
  }, [supported, lat, lon]);

  if (!supported) return <MapUnavailable height={height} note="The charge's figures beside it are unaffected." />;
  return <div ref={ref} className="rounded-lg overflow-hidden border border-neutral-800" style={{ height }} />;
}
