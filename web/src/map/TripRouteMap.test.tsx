import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const loadMapLibre = vi.fn();
vi.mock("./loader", () => ({ loadMapLibre: () => loadMapLibre() }));
const webgl = vi.fn();
vi.mock("./webgl", () => ({ webglAvailable: () => webgl() }));
vi.mock("../lib/config", () => ({
  ensureConfig: () => Promise.resolve(),
  tilesPMTilesURL: () => "/api/maps/tiles/us.pmtiles",
  tilesGlyphsURL: () => "",
  tilesDEM: () => ({ url: "", minZoom: 3, maxZoom: 10 }),
  valhallaBase: () => (globalThis as { __valhalla?: string }).__valhalla ?? "",
  bookingAffiliateID: () => "",
}));
vi.mock("../lib/poi", () => ({ findChargersAlongPath: () => Promise.resolve([]), HOTEL_FACILITY_TYPES: new Set<string>() }));

import { TripRouteMap } from "./TripRouteMap";
import { tripCardText } from "./tripPure";
import type { TripRoute } from "../lib/api";

class FakeMap {
  static last: FakeMap | undefined;
  handlers: Record<string, ((e?: unknown) => void)[]> = {};
  layers = new Set<string>();
  sources = new Map<string, { setData: (d: unknown) => void }>();
  removed = false;
  scrollZoom = { enable: vi.fn(), disable: vi.fn() };
  touchZoomRotate = { disableRotation: vi.fn() };
  constructor(public opts: Record<string, unknown>) {
    FakeMap.last = this;
    queueMicrotask(() => this.fire("style.load"));
  }
  on(ev: string, a?: unknown, b?: unknown) { (this.handlers[ev] ??= []).push((typeof a === "function" ? a : b) as () => void); return this; }
  once(ev: string, fn: () => void) { return this.on(ev, fn); }
  off() { return this; }
  fire(ev: string) { for (const h of this.handlers[ev] ?? []) h(); }
  controlsAdded: unknown[] = [];
  addControl(c: unknown) { this.controlsAdded.push(c); return this; }
  removeControl(c: unknown) { this.controlsAdded = this.controlsAdded.filter((x) => x !== c); return this; }
  getStyle() { return { layers: [] }; }
  images = new Set<string>();
  hasImage(id: string) { return this.images.has(id); }
  addImage(id: string) { this.images.add(id); }
  removeImage(id: string) { this.images.delete(id); }
  project() { return { x: 10, y: 10 }; }
  getContainer() { return { clientWidth: 400, clientHeight: 300 }; }
  setPaintProperty() {}
  addSource(id: string, spec?: { data?: unknown }) { this.sources.set(id, { setData: (d: unknown) => { if (id === "rivolt-track") this.lastData = d; } }); if (id === "rivolt-track") this.lastData = spec?.data; }
  lastData: unknown;
  getSource(id: string) { return this.sources.get(id); }
  addLayer(l: { id: string }) { this.layers.add(l.id); }
  getLayer(id: string) { return this.layers.has(id) ? {} : undefined; }
  // The library's isStyleLoaded() is false while any source is loading a tile; the component must not depend on it.
  isStyleLoaded() { return false; }
  setStyle() { return this; }
  fitBounds = vi.fn();
  easeTo = vi.fn();
  resize = vi.fn();
  triggerRepaint = vi.fn();
  queryRenderedFeatures() { return [{}]; }
  getCanvas() { return document.createElement("canvas"); }
  remove() { this.removed = true; }
}
class FakeMarker {
  static all: { element: HTMLElement }[] = [];
  constructor(public opts: { element: HTMLElement }) { FakeMarker.all.push(opts); }
  get element() { return this.opts.element; }
  setLngLat() { return this; }
  getLngLat() { return { lng: 0, lat: 0 }; }
  getElement() { return this.opts.element; }
  addTo() { return this; }
  remove() {}
}
class FakePopup {
  setLngLat() { return this; }
  setHTML() { return this; }
  addTo() { return this; }
  remove() {}
  getElement() { return null; }
}
class FakeControl {}
class FakeResizeObserver {
  observe() {}
  disconnect() {}
}

const wp = (lat: number, lon: number, name: string) => ({ WaypointType: name === "Stop" ? "charger" : "other", Latitude: lat, Longitude: lon, Name: name, MaxPowerKW: 0, ChargeDurationSec: 0, ArrivalSoC: 80, DepartureSoC: 80, AdapterRequired: false });
// A made-up straight trip near (10, 10).
const route = { Waypoints: [wp(10, 10, "A"), wp(10.5, 10.5, "Stop"), wp(11, 11, "B")] } as unknown as TripRoute;

describe("TripRouteMap", () => {
  beforeEach(() => {
    FakeMarker.all = [];
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    webgl.mockReturnValue(true);
    loadMapLibre.mockResolvedValue({ Map: FakeMap, Marker: FakeMarker, Popup: FakePopup, NavigationControl: FakeControl, AttributionControl: FakeControl });
  });
  afterEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  it("says the map is unavailable without WebGL and never loads MapLibre", () => {
    webgl.mockReturnValue(false);
    render(<TripRouteMap route={route} />);
    expect(screen.getByRole("status").textContent).toMatch(/can't be drawn/);
    expect(loadMapLibre).not.toHaveBeenCalled();
  });

  it("draws a marker per waypoint and reports a click as the waypoint's index", async () => {
    const onSelect = vi.fn();
    render(<TripRouteMap route={route} onSelectStop={onSelect} />);
    await waitFor(() => expect(FakeMarker.all).toHaveLength(3));
    await waitFor(() => expect(FakeMap.last?.layers.has("rivolt-track-main")).toBe(true));
    fireEvent.click(FakeMarker.all[1].element);
    expect(onSelect).toHaveBeenCalledWith(1);
  });

  it("draws a route that arrives after the style loaded, even while tiles are still loading (isStyleLoaded false)", async () => {
    // Valhalla answers after the map and its style exist: four points against the three waypoints, so the snapped path is told from the straight legs.
    const snapped: [number, number][] = [[10, 10], [10.2, 10.3], [10.7, 10.6], [11, 11]];
    const enc = (v: number) => {
      let n = v < 0 ? ~(v << 1) : v << 1;
      let out = "";
      while (n >= 0x20) { out += String.fromCharCode((0x20 | (n & 0x1f)) + 63); n >>= 5; }
      return out + String.fromCharCode(n + 63);
    };
    let pl = 0, po = 0, shape = "";
    for (const [la, lo] of snapped) {
      const a = Math.round(la * 1e6), b = Math.round(lo * 1e6);
      shape += enc(a - pl) + enc(b - po);
      pl = a; po = b;
    }
    (globalThis as { __valhalla?: string }).__valhalla = "/valhalla";
    vi.stubGlobal("fetch", () => new Promise((res) => setTimeout(() => res({ ok: true, json: async () => ({ trip: { legs: [{ shape }] } }) }), 30)));
    try {
      render(<TripRouteMap route={route} />);
      await waitFor(() => expect(FakeMap.last?.layers.has("rivolt-track-main")).toBe(true));
      await waitFor(() => {
        const d = FakeMap.last?.lastData as { features: { geometry: { coordinates: number[][] } }[] } | undefined;
        expect(d?.features[0].geometry.coordinates).toHaveLength(4);
      });
    } finally {
      delete (globalThis as { __valhalla?: string }).__valhalla;
    }
  });

  it("draws the route in one colour with a one-line card: day, distance, stops, time", async () => {
    const r = { ...route, TotalDriveDistanceMeters: 367_000, TotalTripDurationSec: 14_700 } as unknown as TripRoute;
    render(<TripRouteMap route={r} dayLabel="Day 2" />);
    await waitFor(() => expect(FakeMap.last?.layers.has("rivolt-track-main")).toBe(true));
    await waitFor(() => expect(FakeMap.last!.controlsAdded.some((c) => typeof (c as { update?: unknown }).update === "function")).toBe(true));
    // The card's DOM is made when it is added to a real map; check its text through the pure builder the component uses.
    expect(tripCardText({ day: "Day 2", meters: 367_000, stops: 1, seconds: 14_700, unit: "mi" })).toBe("Day 2 · 228 mi · 1 charging stop · 4h 5m");
  });

  it("a charging stop is an amber disc with a bolt; the ends are plain dots", async () => {
    render(<TripRouteMap route={route} />);
    await waitFor(() => expect(FakeMarker.all).toHaveLength(3));
    expect(FakeMarker.all[0].element.textContent).toBe("");
    expect(FakeMarker.all[1].element.textContent).toBe("⚡");
    expect(FakeMarker.all[2].element.textContent).toBe("");
  });

  it("removes the map when it unmounts", async () => {
    const { unmount } = render(<TripRouteMap route={route} />);
    await waitFor(() => expect(FakeMarker.all).toHaveLength(3));
    unmount();
    expect(FakeMap.last?.removed).toBe(true);
  });
});
