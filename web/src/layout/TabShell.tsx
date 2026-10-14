import { Link, NavLink } from "react-router-dom";
import type { TabDef, TabKey } from "./tabs";

// Chrome for the money_tab_enabled shell: a bottom tab bar on mobile,
// a left rail on desktop, and a segmented sub-nav for tabs that group
// several routes. Rendered by AppLayout only when the flag is on.

// Simple 24px stroke icons, inline so the shell adds no dependency.
function TabIcon({ tab }: { tab: TabKey }) {
  const common = {
    width: 22,
    height: 22,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.8,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    "aria-hidden": true,
  };
  switch (tab) {
    case "now":
      // Gauge
      return (
        <svg {...common}>
          <path d="M4 15a8 8 0 1 1 16 0" />
          <path d="M12 15l4-5" />
          <circle cx="12" cy="15" r="1" />
        </svg>
      );
    case "activity":
      // List with route dots
      return (
        <svg {...common}>
          <circle cx="5" cy="6" r="1.5" />
          <circle cx="5" cy="12" r="1.5" />
          <circle cx="5" cy="18" r="1.5" />
          <path d="M9 6h11M9 12h11M9 18h11" />
        </svg>
      );
    case "money":
      // Receipt
      return (
        <svg {...common}>
          <path d="M6 3h12v18l-3-2-3 2-3-2-3 2z" />
          <path d="M9 8h6M9 12h6M9 16h3" />
        </svg>
      );
    case "plan":
      // Map pin route
      return (
        <svg {...common}>
          <path d="M12 21s-6-5.3-6-10a6 6 0 0 1 12 0c0 4.7-6 10-6 10z" />
          <circle cx="12" cy="11" r="2" />
        </svg>
      );
    case "truck":
      // Truck
      return (
        <svg {...common}>
          <path d="M3 16V8h11v8M14 11h4l3 3v2h-7" />
          <circle cx="7" cy="17" r="2" />
          <circle cx="17" cy="17" r="2" />
        </svg>
      );
  }
}

export function BottomTabBar({
  tabs,
  active,
  onPrefetch,
}: {
  tabs: TabDef[];
  active?: TabKey;
  onPrefetch?: (to: string) => void;
}) {
  return (
    <nav
      aria-label="Primary"
      className="app-safe-bottom fixed inset-x-0 bottom-0 z-1100 border-t border-neutral-800 bg-neutral-950/95 backdrop-blur-sm md:hidden"
    >
      <ul className="mx-auto flex max-w-lg items-stretch justify-around">
        {tabs.map((t) => {
          const isActive = t.key === active;
          return (
            <li key={t.key} className="flex-1">
              <Link
                to={t.to}
                aria-current={isActive ? "page" : undefined}
                onTouchStart={() => onPrefetch?.(t.to)}
                onFocus={() => onPrefetch?.(t.to)}
                className={`flex min-h-14 flex-col items-center justify-center gap-0.5 px-1 py-1.5 text-[11px] font-medium transition-colors ${
                  isActive ? "text-emerald-300" : "text-neutral-500 hover:text-neutral-200"
                }`}
              >
                <TabIcon tab={t.key} />
                <span>{t.label}</span>
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

export function SideRail({
  tabs,
  active,
  onPrefetch,
}: {
  tabs: TabDef[];
  active?: TabKey;
  onPrefetch?: (to: string) => void;
}) {
  return (
    <nav
      aria-label="Primary"
      className="sticky top-20 hidden w-44 shrink-0 self-start py-6 md:block"
    >
      <ul className="space-y-1">
        {tabs.map((t) => {
          const isActive = t.key === active;
          return (
            <li key={t.key}>
              <Link
                to={t.to}
                aria-current={isActive ? "page" : undefined}
                onMouseEnter={() => onPrefetch?.(t.to)}
                onFocus={() => onPrefetch?.(t.to)}
                className={`flex items-center gap-3 rounded-lg px-3 py-2 text-sm transition-colors ${
                  isActive
                    ? "bg-neutral-800 text-neutral-100"
                    : "text-neutral-400 hover:bg-neutral-900 hover:text-neutral-100"
                }`}
              >
                <span className={isActive ? "text-emerald-300" : ""}>
                  <TabIcon tab={t.key} />
                </span>
                {t.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

// SubNav is the segmented control above pages whose tab groups more
// than one route (Now: Overview/Live, Activity: Drives/Charges,
// Truck: Settings/Admin). Hidden when the tab has 0–1 destinations.
export function SubNav({
  tab,
  onPrefetch,
}: {
  tab?: TabDef;
  onPrefetch?: (to: string) => void;
}) {
  if (!tab || tab.subs.length < 2) return null;
  return (
    <nav aria-label={`${tab.label} sections`} className="mb-4">
      <ul className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5">
        {tab.subs.map((s) => (
          <li key={s.to}>
            <NavLink
              to={s.to}
              end={s.end}
              onMouseEnter={() => onPrefetch?.(s.to)}
              onFocus={() => onPrefetch?.(s.to)}
              className={({ isActive }) =>
                `block rounded-md px-3 py-1.5 text-sm transition-colors ${
                  isActive
                    ? "bg-neutral-800 text-neutral-100"
                    : "text-neutral-400 hover:text-neutral-100"
                }`
              }
            >
              {s.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );
}
