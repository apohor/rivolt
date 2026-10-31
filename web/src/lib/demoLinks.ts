// "See it in the demo": a plain link to the demo page that shows a Pro feature, for someone who has not subscribed. The sample truck is
// read-only and public, so the link carries nothing about the person: no parameters, no tracking.
const DEMO = "https://demo.rivolt.dev/demo";

export type DemoLink = { label: string; href: string };

export const DEMO_PLANNER: DemoLink = { label: "See the multi-day planner in the demo", href: `${DEMO}/trips/plan` };
export const DEMO_HISTORY: DemoLink = { label: "See full history in the demo", href: `${DEMO}/activity` };
export const DEMO_TRENDS: DemoLink = { label: "See trends in the demo", href: `${DEMO}/truck` };

// The links that fit a limit note: the planner's for a planner, plan-vs-actual or saved-trips limit; history and trends for data
// outside the free window. None for the others.
export function demoLinksFor(o: { feature?: string; code: string }): DemoLink[] {
  if (o.feature === "planner" || o.feature === "plan-vs-actual" || o.feature === "saved-trips") return [DEMO_PLANNER];
  if (!o.feature && o.code === "outside_free_window") return [DEMO_HISTORY, DEMO_TRENDS];
  return [];
}
