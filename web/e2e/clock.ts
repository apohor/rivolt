import type { Page } from "@playwright/test";

// The e2e suite's "now". Normally the real clock, so fixtures stay minutes old
// for the whole run. With STUB_NOW (an RFC 3339 time) every fixture is placed
// relative to that instant instead, and pinClock fixes the browser's clock to
// it too, so a run can be made to start at 23:55 on a month's last day or
// 00:05 on the 1st: the moments date-bound specs break.
const pinnedAt = Date.parse(process.env.STUB_NOW ?? "");

export const clockPinned = Number.isFinite(pinnedAt);

export function e2eNow(): number {
  return clockPinned ? pinnedAt : Date.now();
}

// pinClock fixes the page's Date to STUB_NOW (timers still run). A no-op on a
// normal run. Call it before page.goto; stubApi does.
export async function pinClock(page: Page): Promise<void> {
  if (clockPinned) await page.clock.setFixedTime(pinnedAt);
}
