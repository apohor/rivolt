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
  addControl(c: { constructor: { name: string }; el?: HTMLElement }, pos: string) { this.controls.push(`${pos}:${(c as { __n?: string }).__n ?? c.constructor.name}`); return this; }
  addSource(id: string) { this.sources.set(id, { setData: vi.fn() }); }
  getSource(id: string) { return this.sources.get(id); }
  addLayer(l: { id: string }) { this.layers.add(l.id); }
  getLayer(id: string) { return this.layers.has(id) ? {} : undefined; }
  setStyle(s: unknown) { this.styleCalls.push(s); this.layers.clear(); this.sources.clear(); queueMicrotask(() => this.fire("style.load")); }
  isStyleLoaded() { return true; }
  jumpTo = vi.fn();
  fitBounds = vi.fn();
  resize = vi.fn();
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

beforeEach(() => {
  FakeMarker.all = [];
  FakeMap.last = undefined;
  loadMapLibre.mockResolvedValue(fakeML);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  document.documentElement.setAttribute("data-theme", "dark");
});
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks(); });

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
    await waitFor(() => expect(FakeMap.last!.layers.has("rivolt-route-main")).toBe(true));
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
    await waitFor(() => expect(FakeMap.last!.layers.has("rivolt-route-main")).toBe(true));
    const m = FakeMap.last!;
    document.documentElement.setAttribute("data-theme", "light");
    await waitFor(() => expect(m.styleCalls).toHaveLength(2));
    expect(JSON.stringify(m.styleCalls[0])).not.toEqual(JSON.stringify(m.styleCalls[1]));
    await waitFor(() => expect(m.layers.has("rivolt-route-main")).toBe(true));
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
