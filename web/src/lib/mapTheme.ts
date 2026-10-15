// The basemap follows the resolved app theme. The theme is applied as
// html[data-theme="light"] (see theme.ts; it is never set with the shell
// flag off, which is the dark app), so reading that attribute covers
// System, an explicit choice and a live OS switch alike.

export type MapFlavor = "dark" | "light";

export function appMapFlavor(root: HTMLElement = document.documentElement): MapFlavor {
  return root.getAttribute("data-theme") === "light" ? "light" : "dark";
}

// onAppMapFlavor calls cb with the new flavor whenever the app theme
// flips. Returns the unsubscribe.
export function onAppMapFlavor(cb: (f: MapFlavor) => void, root: HTMLElement = document.documentElement): () => void {
  if (typeof MutationObserver === "undefined") return () => {};
  let last = appMapFlavor(root);
  const mo = new MutationObserver(() => {
    const next = appMapFlavor(root);
    if (next === last) return;
    last = next;
    cb(next);
  });
  mo.observe(root, { attributes: true, attributeFilter: ["data-theme"] });
  return () => mo.disconnect();
}
