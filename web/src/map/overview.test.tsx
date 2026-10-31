import { render, screen, waitFor } from "@testing-library/react";
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
}));
vi.mock("../lib/poi", () => ({ findNearestCharger: () => Promise.resolve(null) }));

import { ChargeMap } from "./ChargeMap";
import { ChargesOverviewMap } from "./ChargesOverviewMap";
import { DrivesOverviewMap } from "./DrivesOverviewMap";

type Handler = (e: unknown) => void;
class FakeMap {
  static last: FakeMap | undefined;
  handlers: Record<string, Handler[]> = {};
  layerHandlers: Record<string, Handler[]> = {};
  layers = new Set<string>();
  sources = new Map<string, { data: unknown; setData: (d: unknown) => void }>();
  removed = false;
  scrollZoom = { enable: vi.fn(), disable: vi.fn() };
  touchZoomRotate = { disableRotation: vi.fn() };
  fitBounds = vi.fn();
  easeTo = vi.fn();
  resize = vi.fn();
  constructor(public opts: Record<string, unknown>) {
    FakeMap.last = this;
    queueMicrotask(() => this.fire("style.load"));
  }
  // map.on(ev, fn) and map.on(ev, layer, fn)
  on(ev: string, a: unknown, b?: unknown) {
    if (typeof a === "function") (this.handlers[ev] ??= []).push(a as Handler);
    else (this.layerHandlers[`${ev}:${a as string}`] ??= []).push(b as Handler);
    return this;
  }
  fire(ev: string, e?: unknown) { for (const h of this.handlers[ev] ?? []) h(e); }
  fireLayer(ev: string, layer: string, e: unknown) { for (const h of this.layerHandlers[`${ev}:${layer}`] ?? []) h(e); }
  off() { return this; }
  addControl() { return this; }
  addSource(id: string, spec: { data: unknown }) {
    const src = { data: spec.data, setData: (d: unknown) => { src.data = d; } };
    this.sources.set(id, src);
  }
  getSource(id: string) { return this.sources.get(id); }
  addLayer(l: { id: string }) { this.layers.add(l.id); }
  getLayer(id: string) { return this.layers.has(id) ? {} : undefined; }
  setStyle() { return this; }
  getCanvas() { return document.createElement("canvas"); }
  getBounds() { return { contains: () => true }; }
  remove() { this.removed = true; }
}
class FakeMarker {
  static all: FakeMarker[] = [];
  removed = false;
  constructor(public opts: { element: HTMLElement }) { FakeMarker.all.push(this); }
  setLngLat() { return this; }
  setPopup() { return this; }
  addTo() { return this; }
  remove() { this.removed = true; }
}
class FakePopup {
  static all: { html: string; removed: boolean }[] = [];
  rec = { html: "", removed: false };
  constructor() { FakePopup.all.push(this.rec); }
  setLngLat() { return this; }
  setHTML(h: string) { this.rec.html = h; return this; }
  setText(t: string) { this.rec.html = t; return this; }
  addTo() { return this; }
  remove() { this.rec.removed = true; }
}
class FakeControl {}
class FakeResizeObserver { observe() {} disconnect() {} }

const charges = [
  { ID: "c1", Lat: 10, Lon: 10, StartedAt: "2026-01-01T00:00:00Z", EnergyAddedKWh: 20, MaxPowerKW: 150 },
  { ID: "c2", Lat: 10.1, Lon: 10.1, StartedAt: "2026-01-02T00:00:00Z", EnergyAddedKWh: 8, MaxPowerKW: 7 },
];
const drives = [
  { ID: "d1", StartLat: 10, StartLon: 10, EndLat: 10.5, EndLon: 10.5, StartedAt: "2026-01-01T00:00:00Z", DistanceMi: 30 },
  { ID: "d2", StartLat: 10.2, StartLon: 10.2, EndLat: 10.6, EndLon: 10.4, StartedAt: "2026-01-02T00:00:00Z", DistanceMi: 12 },
];

beforeEach(() => {
  FakeMarker.all = [];
  FakePopup.all = [];
  FakeMap.last = undefined;
  webgl.mockReturnValue(true);
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  loadMapLibre.mockResolvedValue({ Map: FakeMap, Marker: FakeMarker, Popup: FakePopup, NavigationControl: FakeControl, AttributionControl: FakeControl });
});
afterEach(() => {
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe("overview and charge maps", () => {
  it("each says the map is unavailable without WebGL and never loads MapLibre", () => {
    webgl.mockReturnValue(false);
    render(<><ChargesOverviewMap charges={charges} /><DrivesOverviewMap drives={drives} /><ChargeMap lat={10} lon={10} /></>);
    expect(screen.getAllByRole("status")).toHaveLength(3);
    expect(loadMapLibre).not.toHaveBeenCalled();
  });

  it("charges: one dot per charge, a click on a dot selects that charge", async () => {
    const onSelect = vi.fn();
    render(<ChargesOverviewMap charges={charges} onSelect={onSelect} />);
    await waitFor(() => expect(FakeMap.last?.layers.has("rivolt-charges-dots")).toBe(true));
    const data = FakeMap.last!.sources.get("rivolt-charges")!.data as { features: { properties: { id: string; color: string } }[] };
    expect(data.features.map((f) => f.properties.id)).toEqual(["c1", "c2"]);
    expect(data.features[0].properties.color).toBe("#f43f5e"); // 150 kW: fast
    expect(data.features[1].properties.color).toBe("#06b6d4"); // 7 kW: level 2
    FakeMap.last!.fireLayer("click", "rivolt-charges-dots", { features: [{ properties: { id: "c2" } }] });
    expect(onSelect).toHaveBeenCalledWith("c2");
  });

  it("drives: a line per drive and start and end dots, a click selects the drive", async () => {
    const onSelect = vi.fn();
    render(<DrivesOverviewMap drives={drives} onSelect={onSelect} />);
    await waitFor(() => expect(FakeMap.last?.layers.has("rivolt-drives-line")).toBe(true));
    const lines = FakeMap.last!.sources.get("rivolt-drives")!.data as { features: { geometry: { coordinates: number[][] } }[] };
    expect(lines.features).toHaveLength(2);
    expect(lines.features[0].geometry.coordinates[0]).toEqual([10, 10]); // [lon, lat]
    const ends = FakeMap.last!.sources.get("rivolt-drive-ends")!.data as { features: unknown[] };
    expect(ends.features).toHaveLength(4);
    FakeMap.last!.fireLayer("click", "rivolt-drives-hit", { features: [{ properties: { id: "d1" } }] });
    expect(onSelect).toHaveBeenCalledWith("d1");
  });

  it("charge: the recorded pin, labelled with the user's own network, and torn down on unmount", async () => {
    const { unmount } = render(<ChargeMap lat={10} lon={10} ownLabel="Home charger" />);
    await waitFor(() => expect(FakeMarker.all.length).toBe(1));
    await waitFor(() => expect(FakePopup.all.some((p) => p.html.includes("Home charger"))).toBe(true));
    unmount();
    expect(FakeMap.last?.removed).toBe(true);
  });
});
