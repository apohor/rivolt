// New-version detection: the page compares the server's reported version with the
// one it was built (or first loaded) with. Pure logic here; the component owns
// timers, visibility and reload.

export const VERSION_POLL_MS = 60_000;

// The version this page belongs to: whatever the server reported on the first
// check after load. An open tab sees a deploy as a later check that differs.
export function baselineVersion(firstSeen: string | null): string | null {
  return firstSeen && firstSeen.trim() ? firstSeen.trim() : null;
}

export function isNewVersion(baseline: string | null, live: string | null | undefined): boolean {
  if (!baseline || !live) return false;
  return baseline !== live.trim();
}

export function isPreviewHost(hostname: string): boolean {
  return hostname === "preview.rivolt.dev";
}

// "reload": preview only, and only while the tab is hidden (the owner sees the new
// build the moment he switches back). "prompt": everywhere else, never an
// auto-reload (prod users decide). "none": versions match or are unknown.
export function nextAction(o: {
  baseline: string | null;
  live: string | null | undefined;
  hidden: boolean;
  preview: boolean;
}): "none" | "prompt" | "reload" {
  if (!isNewVersion(o.baseline, o.live)) return "none";
  return o.preview && o.hidden ? "reload" : "prompt";
}
