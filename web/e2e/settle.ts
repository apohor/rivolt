import type { Page } from "@playwright/test";

// Resolves when the page has stopped moving: the scroll position and the boxes of everything matching `selectors` (and the
// Leaflet markers in the plan map) are the same for `frames` animation frames in a row. A fixed wait is a guess at how long a
// smooth scroll and a map fit take; on a slow WebKit runner it was too short now and then. Fails after `timeoutMs`.
export async function settled(page: Page, selectors: string[], { frames = 8, timeoutMs = 10_000 } = {}): Promise<void> {
  await page.evaluate(
    ({ selectors, frames, timeoutMs }) =>
      new Promise<void>((resolve, reject) => {
        const started = performance.now();
        let last = "";
        let same = 0;
        const snapshot = () => {
          const boxes = [...selectors.flatMap((s) => [...document.querySelectorAll(s)]), ...document.querySelectorAll("[data-plan-area='map'] .leaflet-marker-icon")].map((el) => {
            const r = el.getBoundingClientRect();
            return `${Math.round(r.top * 10)},${Math.round(r.left * 10)},${Math.round(r.width * 10)},${Math.round(r.height * 10)}`;
          });
          return `${window.scrollY}|${boxes.join(";")}`;
        };
        const tick = () => {
          const now = snapshot();
          same = now === last ? same + 1 : 0;
          last = now;
          if (same >= frames) return resolve();
          if (performance.now() - started > timeoutMs) return reject(new Error("the page never stopped moving"));
          requestAnimationFrame(tick);
        };
        tick();
      }),
    { selectors, frames, timeoutMs },
  );
}
