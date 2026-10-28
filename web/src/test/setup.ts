import "@testing-library/jest-dom/vitest";

// Node 25+ ships its own Storage, localStorage and sessionStorage globals. Without
// --localstorage-file they have no backing store, and vitest does not overwrite them with
// the jsdom ones, so window.localStorage is undefined and spying on Storage.prototype
// misses. Point them back at jsdom's, which every test here is written against. A no-op on
// Node versions without those globals.
type JsdomGlobal = { jsdom?: { window: Window & typeof globalThis } };
const dom = (globalThis as unknown as JsdomGlobal).jsdom?.window;
if (dom) {
  for (const name of ["Storage", "localStorage", "sessionStorage"] as const) {
    Object.defineProperty(globalThis, name, { value: dom[name], configurable: true, writable: true });
  }
}
