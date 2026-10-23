import { useCallback, type KeyboardEvent, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";

// Plan sections, as tabs at the top of /plan. The selected tab
// lives in the URL (?tab=) so a reload, a link and the back button agree.
export type PlanTab = "planner" | "saved" | "pva";

const TAB_IDS: readonly PlanTab[] = ["planner", "saved", "pva"];

// parsePlanTab maps the raw ?tab= value to a tab: unknown or missing values
// fall back to the planner.
export function parsePlanTab(raw: string | null): PlanTab {
  return TAB_IDS.find((id) => id === raw) ?? "planner";
}

// usePlanTab is [tab, setTab] backed by ?tab=. Choosing the planner removes
// the param (a clean URL); other query params are kept; the history entry is
// replaced, so switching tabs does not fill the back stack.
export function usePlanTab(): [PlanTab, (t: PlanTab) => void] {
  const [params, setParams] = useSearchParams();
  const tab = parsePlanTab(params.get("tab"));
  const setTab = useCallback(
    (t: PlanTab) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (t === "planner") next.delete("tab");
          else next.set("tab", t);
          return next;
        },
        { replace: true },
      );
    },
    [setParams],
  );
  return [tab, setTab];
}

export interface PlanTabSpec {
  id: PlanTab;
  label: ReactNode;
  count?: number;
}

// nextTabIndex is the roving-focus target for a key press, or null when the
// key is not a tab-navigation key. Arrow keys wrap; Home and End jump.
export function nextTabIndex(key: string, current: number, n: number): number | null {
  if (n <= 0) return null;
  switch (key) {
    case "ArrowRight":
    case "ArrowDown":
      return (current + 1) % n;
    case "ArrowLeft":
    case "ArrowUp":
      return (current - 1 + n) % n;
    case "Home":
      return 0;
    case "End":
      return n - 1;
    default:
      return null;
  }
}

// DOM ids are fixed: there is one planner tablist per page.
export const planTabId = (t: PlanTab) => `plan-tab-${t}`;
export const planPanelId = (t: PlanTab) => `plan-tabpanel-${t}`;

// PlanTabs is an ARIA tablist with automatic activation: arrow keys move focus
// and select, and only the active tab is in the tab order (roving tabindex).
// Render each section with <PlanTabPanel>, which points back here.
export function PlanTabs({
  tabs,
  value,
  onChange,
  label = "Trip planner sections",
  className,
}: {
  tabs: PlanTabSpec[];
  value: PlanTab;
  onChange: (t: PlanTab) => void;
  label?: string;
  className?: string;
}) {
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const i = tabs.findIndex((t) => t.id === value);
    const to = nextTabIndex(e.key, i < 0 ? 0 : i, tabs.length);
    if (to == null) return;
    e.preventDefault();
    const target = tabs[to];
    onChange(target.id);
    document.getElementById(planTabId(target.id))?.focus();
  };
  return (
    <div role="tablist" aria-label={label} data-plan-tabs className={className} onKeyDown={onKeyDown}>
      {tabs.map((t) => {
        const active = t.id === value;
        return (
          <button
            key={t.id}
            id={planTabId(t.id)}
            type="button"
            role="tab"
            aria-selected={active}
            aria-controls={planPanelId(t.id)}
            tabIndex={active ? 0 : -1}
            data-tab-active={active ? "true" : undefined}
            onClick={() => onChange(t.id)}
          >
            {t.label}
            {t.count != null && t.count > 0 && <span data-tab-count> {t.count}</span>}
          </button>
        );
      })}
    </div>
  );
}

// PlanTabPanel is the tabpanel for one tab. Only the active panel is
// rendered, so inactive sections cost nothing and cannot be reached by
// keyboard.
export function PlanTabPanel({ tab, active, children, className }: { tab: PlanTab; active: boolean; children: ReactNode; className?: string }) {
  if (!active) return null;
  return (
    <div role="tabpanel" id={planPanelId(tab)} aria-labelledby={planTabId(tab)} className={className}>
      {children}
    </div>
  );
}
