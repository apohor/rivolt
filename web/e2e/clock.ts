import type { Page } from "@playwright/test";

// The e2e suite's "now". Fixed, so no spec depends on the real date: every fixture is placed relative to
// this instant and pinClock fixes the browser's clock to it too. Left to the real clock, the suite broke
// in the first hours of every month (the stub's rows are a few hours to days old, so on the 1st the
// current month held only the newest few, which the Activity feed folds). The default is a Tuesday in the
// middle of a month, midday UTC and early morning Pacific. STUB_NOW (an RFC 3339 time) sets another instant:
// the nightly run uses it for the boundaries (23:55 on a month's last day, 00:05 on the 1st).
export const DEFAULT_NOW = "2026-11-17T18:00:00Z";
const pinnedAt = Date.parse(process.env.STUB_NOW || DEFAULT_NOW);

export const clockPinned = Number.isFinite(pinnedAt);

export function e2eNow(): number {
  return clockPinned ? pinnedAt : Date.now();
}

// pinClock fixes the page's Date to STUB_NOW (timers still run). A no-op on a
// normal run. Call it before page.goto; stubApi does.
export async function pinClock(page: Page): Promise<void> {
  if (clockPinned) await page.clock.setFixedTime(pinnedAt);
}
