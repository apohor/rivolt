// One demo_view per page load, for visits that land on the demo (a /demo
// URL, or any entry URL on the standalone demo host). The entry URL and
// referrer are read when this module loads, before the standalone demo's
// redirect to /demo drops the query string. Same track() path as every
// other event, so Do-Not-Track / Global Privacy Control send nothing.

import { track } from "./track";
import { visitSourceProps } from "./visitSource";

const entry =
  typeof window === "undefined"
    ? null
    : { path: window.location.pathname, search: window.location.search, referrer: document.referrer };

let sent = false;

export function trackDemoVisit(demoOnlyHost: boolean) {
  if (sent || !entry) return;
  const demoEntry = entry.path.startsWith("/demo") || (demoOnlyHost && entry.path === "/");
  if (!demoEntry) return;
  sent = true;
  track("demo_view", visitSourceProps(entry.search, entry.referrer));
}
