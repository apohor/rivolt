import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const loadMapLibre = vi.fn();
vi.mock("./loader", () => ({ loadMapLibre: () => loadMapLibre() }));
const webgl = vi.fn();
vi.mock("./webgl", () => ({ webglAvailable: () => webgl() }));
vi.mock("../lib/config", () => ({
  ensureConfig: () => Promise.resolve(),
  tilesPMTilesURL: () => "/api/maps/tiles/us.pmtiles",
  tilesGlyphsURL: () => "/api/maps/glyphs/{fontstack}/{range}.pbf",
  tilesDEM: () => ({ url: "/api/maps/dem/{z}/{x}/{y}.png", minZoom: 3, maxZoom: 10 }),
  valhallaBase: () => "",
}));

import { DriveMap } from "./DriveMap";

// A MapLibre stand-in that records what the component asks of it; jsdom has no WebGL to render with.
class FakeMap {
  static last: FakeMap | undefined;
  handlers: Record<string, ((e?: unknown) => void)[]> = {};
  controls: string[] = [];
  removed = false;
  styleCalls: unknown[] = [];
  options: Record<string, unknown>;
  layers = new Set<string>();
  sources = new Map<string, { setData: (d: unknown) => void }>();
  scrollZoom = { enable: vi.fn(), disable: vi.fn() };
  touchZoomRotate = { disableRotation: vi.fn() };
  constructor(opts: Record<string, unknown>) {
    this.options = opts;
    this.styleCalls.push(opts.style);
    FakeMap.last = this;
    queueMicrotask(() => this.fire("style.load"));
  }
  on(ev: string, fn: (e?: unknown) => void) { (this.handlers[ev] ??= []).push(fn); return this; }
  once(ev: string, fn: (e?: unknown) => void) { return this.on(ev, fn); }
  fire(ev: string) { for (const h of this.handlers[ev] ?? []) h(); }
  addControl(c: { constructor: { name: string }; onAdd?: (m: unknown) => HTMLElement }, pos: string) {
    this.controls.push(`${pos}:${(c as { __n?: string }).__n ?? c.constructor.name}`);
    // Our own controls are real; mount their DOM so a test can read it.
    if (typeof c.onAdd === "function") document.body.appendChild(c.onAdd(this));
    return this;
  }
  paint = new Map<string, unknown>();
  setPaintProperty(id: string, prop: string, v: unknown) { this.paint.set(`${id}:${prop}`, v); }
  getStyle() { return { layers: [] }; }
  images = new Set<string>();
  hasImage(id: string) { return this.images.has(id); }
  addImage(id: string) { this.images.add(id); }
  removeImage(id: string) { this.images.delete(id); }
  addSource(id: string) { this.sources.set(id, { setData: vi.fn() }); }
  getSource(id: string) { return this.sources.get(id); }
  addLayer(l: { id: string }) { this.layers.add(l.id); }
  getLayer(id: string) { return this.layers.has(id) ? {} : undefined; }
  setStyle(s: unknown) { this.styleCalls.push(s); this.layers.clear(); this.sources.clear(); queueMicrotask(() => this.fire("style.load")); }
  isStyleLoaded() { return true; }
  jumpTo = vi.fn();
  fitBounds = vi.fn();
  resize = vi.fn();
  triggerRepaint = vi.fn();
  queryRenderedFeatures() { return []; }
  off() { return this; }
  project() { return { x: 0, y: 0 }; }
  getContainer() { return document.body; }
  remove() { this.removed = true; }
}
class FakeMarker {
  static all: FakeMarker[] = [];
  constructor(public opts: { element: HTMLElement }) { FakeMarker.all.push(this); }
  setLngLat(ll: [number, number]) { this.ll = ll; return this; }
  ll: [number, number] | undefined;
  addTo() { return this; }
  remove() { FakeMarker.all = FakeMarker.all.filter((m) => m !== this); }
}
const ctl = (n: string) => class { __n = n; };
const fakeML = { Map: FakeMap, Marker: FakeMarker, NavigationControl: ctl("nav"), AttributionControl: ctl("attribution") };

const line = Array.from({ length: 6 }, (_, i) => ({ lat: 40 + i * 0.01, lon: -100 - i * 0.01, t: 1000 + i * 10, s: 20 + i * 10 }));
// A made-up long drive near (10, 10) with speed, altitude, battery and odometer on every sample: 12 miles, 14 points of battery.
const full = Array.from({ length: 121 }, (_, i) => ({ lat: 10 + i * 0.0015, lon: 10, t: 1000 + i * 20, s: 40 + (i % 5) * 5, alt: 100 + i * 2, soc: 80 - i * 0.12, odo: 5000 + i * 0.1 }));

beforeEach(() => {
  FakeMarker.all = [];
  FakeMap.last = undefined;
  loadMapLibre.mockResolvedValue(fakeML);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  document.documentElement.setAttribute("data-theme", "dark");
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  document.body.querySelectorAll(".maplibregl-ctrl").forEach((e) => e.remove());
});

describe("DriveMap", () => {
  it("says the map is unavailable, and loads nothing, when WebGL is not available", () => {
    webgl.mockReturnValue(false);
    render(<DriveMap points={line} />);
    expect(screen.getByRole("status").textContent).toMatch(/can.t be drawn in this browser/);
    expect(loadMapLibre).not.toHaveBeenCalled();
  });

  it("builds the self-hosted style, the route and the pins, and tidies up on unmount", async () => {
    webgl.mockReturnValue(true);
    const { unmount } = render(<DriveMap points={line} />);
    await waitFor(() => expect(FakeMap.last).toBeDefined());
    await waitFor(() => expect(FakeMap.last!.layers.has("rivolt-track-main")).toBe(true));
    const m = FakeMap.last!;
    const style = JSON.stringify(m.styleCalls[0]);
    expect(style).toContain("pmtiles://" + location.origin + "/api/maps/tiles/us.pmtiles");
    expect(style).toContain(location.origin + "/api/maps/dem/{z}/{x}/{y}.png");
    expect(style).not.toContain("sprite");
    expect(m.controls).toEqual(expect.arrayContaining(["top-left:nav", "bottom-right:attribution"]));
    expect(FakeMarker.all.map((k) => k.opts.element.title)).toEqual(["Start", "End"]);
    expect(m.options.attributionControl).toBe(false);
    expect(m.scrollZoom.disable).toHaveBeenCalled();
    unmount();
    expect(m.removed).toBe(true);
    expect(FakeMarker.all).toHaveLength(0);
  });

  it("a flavour change restyles the map and puts the route back", async () => {
    webgl.mockReturnValue(true);
    render(<DriveMap points={line} />);
    await waitFor(() => expect(FakeMap.last!.layers.has("rivolt-track-main")).toBe(true));
    const m = FakeMap.last!;
    document.documentElement.setAttribute("data-theme", "light");
    await waitFor(() => expect(m.styleCalls).toHaveLength(2));
    expect(JSON.stringify(m.styleCalls[0])).not.toEqual(JSON.stringify(m.styleCalls[1]));
    await waitFor(() => expect(m.layers.has("rivolt-track-main")).toBe(true));
  });

  it("offers Speed, Efficiency and Elevation when the drive has the values, and a mode switch repaints the gradient", async () => {
    webgl.mockReturnValue(true);
    render(<DriveMap points={full} packKWh={135} />);
    await waitFor(() => expect(FakeMap.last!.layers.has("rivolt-track-main")).toBe(true));
    const buttons = [...document.querySelectorAll<HTMLButtonElement>(".rivolt-legend [aria-label='Route colour'] button")];
    expect(buttons.map((b) => b.textContent)).toEqual(["Speed", "Efficiency", "Elevation"]);
    expect(document.querySelector(".rivolt-legend")?.textContent).toMatch(/Speed/);
    buttons[1].click();
    expect(FakeMap.last!.paint.get("rivolt-track-main:line-gradient")).toBeDefined();
    expect(document.querySelector(".rivolt-legend")?.getAttribute("data-mode")).toBe("eff");
    expect(document.querySelector(".rivolt-legend")?.textContent).toMatch(/Efficiency \(from battery change\)/);
  });

  it("on a phone the D/L picker is inside the legend card, not a separate control", async () => {
    webgl.mockReturnValue(true);
    render(<DriveMap points={full} packKWh={135} />);
    await waitFor(() => expect(FakeMap.last!.layers.has("rivolt-track-main")).toBe(true));
    // jsdom's container is 0 px wide: a phone.
    expect(FakeMap.last!.controls.some((c) => c.endsWith(":flavorControl") || c.startsWith("top-right"))).toBe(false);
    const dl = [...document.querySelectorAll<HTMLButtonElement>(".rivolt-legend .rivolt-flavor-control button")];
    expect(dl.map((b) => b.title)).toEqual(["Dark basemap", "Light basemap"]);
    dl[1].click();
    await waitFor(() => expect(FakeMap.last!.styleCalls).toHaveLength(2));
  });

  it("hides the switch when only one mode qualifies", async () => {
    webgl.mockReturnValue(true);
    render(<DriveMap points={line} />);
    await waitFor(() => expect(FakeMap.last!.layers.has("rivolt-track-main")).toBe(true));
    expect(document.querySelector(".rivolt-legend [aria-label='Route colour'] button")).toBeNull();
    expect(document.querySelector(".rivolt-legend")?.getAttribute("data-mode")).toBe("speed");
  });

  it("hides Efficiency without a pack size and Elevation without altitude", async () => {
    webgl.mockReturnValue(true);
    render(<DriveMap points={full.map((p) => ({ ...p, alt: undefined }))} />);
    await waitFor(() => expect(FakeMap.last!.layers.has("rivolt-track-main")).toBe(true));
    expect(document.querySelector(".rivolt-legend [aria-label='Route colour'] button")).toBeNull();
  });

  it("shows a drive with one frozen fix as a point, not a route", async () => {
    webgl.mockReturnValue(true);
    const one = [{ lat: 40, lon: -100, t: 1, s: 0 }, { lat: 40, lon: -100, t: 2, s: 0 }];
    render(<DriveMap points={one} />);
    await waitFor(() => expect(FakeMap.last).toBeDefined());
    expect(FakeMap.last!.jumpTo).toHaveBeenCalledWith(expect.objectContaining({ zoom: 16 }));
  });

  it("starts nothing for a drive with no usable position", async () => {
    webgl.mockReturnValue(true);
    render(<DriveMap points={[{ lat: 0, lon: 0 }]} />);
    await new Promise((r) => setTimeout(r, 10));
    expect(FakeMap.last).toBeUndefined();
  });
});
