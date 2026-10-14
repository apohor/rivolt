// Tab model for the money_tab_enabled shell: five top-level tabs
// (Now · Activity · Money · Plan · Truck), each owning one or more
// existing routes. Pure data + pure functions so the active-tab logic
// is unit-testable without rendering the layout.
//
// The tabs map 1:1 onto routes that already exist; nothing moves on
// disk. Where a tab groups several routes (Activity = drives +
// charges), the layout renders a small sub-nav above the page.

export type TabKey = "now" | "activity" | "money" | "plan" | "truck";

export type SubLink = { to: string; label: string; end?: boolean };

export type TabDef = {
  key: TabKey;
  label: string;
  // Where tapping the tab goes.
  to: string;
  // Route prefixes that light this tab up. "/" is matched exactly.
  match: string[];
  // Sub-destinations shown as a segmented control when more than one.
  subs: SubLink[];
};

export type TabContext = {
  tripPlannerEnabled: boolean;
  isAdmin: boolean;
  // Live is hidden while impersonating (see AppLayout / LivePage).
  liveAvailable: boolean;
};

export function buildTabs(ctx: TabContext): TabDef[] {
  const now: TabDef = {
    key: "now",
    label: "Now",
    to: "/",
    match: ["/", "/live"],
    subs: [
      { to: "/", label: "Overview", end: true },
      ...(ctx.liveAvailable ? [{ to: "/live", label: "Live" }] : []),
    ],
  };
  const activity: TabDef = {
    key: "activity",
    label: "Activity",
    to: "/drives",
    match: ["/drives", "/charges"],
    subs: [
      { to: "/drives", label: "Drives" },
      { to: "/charges", label: "Charges" },
    ],
  };
  const money: TabDef = {
    key: "money",
    label: "Money",
    to: "/money",
    match: ["/money"],
    subs: [],
  };
  const plan: TabDef = {
    key: "plan",
    label: "Plan",
    to: "/trips/plan",
    match: ["/trips"],
    subs: [],
  };
  const truck: TabDef = {
    key: "truck",
    label: "Truck",
    to: "/truck",
    match: ["/truck", "/settings", "/admin"],
    subs: [
      { to: "/truck", label: "Health" },
      { to: "/settings", label: "Settings" },
      ...(ctx.isAdmin ? [{ to: "/admin", label: "Admin" }] : []),
    ],
  };
  return [now, activity, money, ...(ctx.tripPlannerEnabled ? [plan] : []), truck];
}

function matches(prefix: string, pathname: string): boolean {
  if (prefix === "/") return pathname === "/";
  return pathname === prefix || pathname.startsWith(prefix + "/");
}

// activeTab returns the tab owning the current path, or undefined for
// routes outside the shell's map (e.g. the 404 page).
export function activeTab(tabs: TabDef[], pathname: string): TabDef | undefined {
  return tabs.find((t) => t.match.some((m) => matches(m, pathname)));
}
