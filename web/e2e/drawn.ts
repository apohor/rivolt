import type { Locator, Page } from "@playwright/test";

// The route is drawn once the map renders at least one feature of it (data-map-drawn above zero, src/map/drawn.ts). On a loaded WebKit runner the
// map's software-rendered first frames can take well over the 5 s an assertion gets by default (a 6.7 s draw under load, none ever failing to
// draw), so a wait for it gets this much instead; a map that never draws still fails, just later.
export const DRAWN_MS = 20_000;

export const drawnMap = (scope: Page | Locator): Locator => scope.locator("[data-map-drawn]:not([data-map-drawn='0'])").first();
