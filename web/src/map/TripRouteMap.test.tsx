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
  valhallaBase: () => "",
  bookingAffiliateID: () => "",
}));
vi.mock("../lib/poi", () => ({ findChargersAlongPath: () => Promise.resolve([]), HOTEL_FACILITY_TYPES: new Set<string>() }));

import { TripRouteMap } from "./TripRouteMap";
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
  addControl() { return this; }
  addSource(id: string) { this.sources.set(id, { setData: vi.fn() }); }
  getSource(id: string) { return this.sources.get(id); }
  addLayer(l: { id: string }) { this.layers.add(l.id); }
  getLayer(id: string) { return this.layers.has(id) ? {} : undefined; }
  isStyleLoaded() { return true; }
  setStyle() { return this; }
  fitBounds = vi.fn();
  easeTo = vi.fn();
  resize = vi.fn();
  triggerRepaint = vi.fn();
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

const wp = (lat: number, lon: number, name: string) => ({ Latitude: lat, Longitude: lon, Name: name, MaxPowerKW: 0, ChargeDurationSec: 0, ArrivalSoC: 80, DepartureSoC: 80, AdapterRequired: false });
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
    await waitFor(() => expect(FakeMap.last?.layers.has("rivolt-route-main")).toBe(true));
    fireEvent.click(FakeMarker.all[1].element);
    expect(onSelect).toHaveBeenCalledWith(1);
  });

  it("removes the map when it unmounts", async () => {
    const { unmount } = render(<TripRouteMap route={route} />);
    await waitFor(() => expect(FakeMarker.all).toHaveLength(3));
    unmount();
    expect(FakeMap.last?.removed).toBe(true);
  });
});
