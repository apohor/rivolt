import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { nextTabIndex, parsePlanTab, PlanTabPanel, PlanTabs, usePlanTab, type PlanTab } from "./PlanTabs";

afterEach(cleanup);

describe("parsePlanTab", () => {
  it("defaults to the planner and ignores unknown values", () => {
    expect(parsePlanTab(null, true)).toBe("planner");
    expect(parsePlanTab("", true)).toBe("planner");
    expect(parsePlanTab("nonsense", true)).toBe("planner");
    expect(parsePlanTab("PLANNER", true)).toBe("planner");
  });
  it("accepts saved always and pva only with the Money shell", () => {
    expect(parsePlanTab("saved", false)).toBe("saved");
    expect(parsePlanTab("pva", true)).toBe("pva");
    expect(parsePlanTab("pva", false)).toBe("planner");
  });
});

describe("nextTabIndex", () => {
  it("moves with the arrows, wraps, and jumps with Home/End", () => {
    expect(nextTabIndex("ArrowRight", 0, 3)).toBe(1);
    expect(nextTabIndex("ArrowRight", 2, 3)).toBe(0);
    expect(nextTabIndex("ArrowLeft", 0, 3)).toBe(2);
    expect(nextTabIndex("ArrowLeft", 2, 3)).toBe(1);
    expect(nextTabIndex("Home", 2, 3)).toBe(0);
    expect(nextTabIndex("End", 0, 3)).toBe(2);
  });
  it("ignores other keys and an empty list", () => {
    expect(nextTabIndex("Enter", 0, 3)).toBeNull();
    expect(nextTabIndex("a", 0, 3)).toBeNull();
    expect(nextTabIndex("ArrowRight", 0, 0)).toBeNull();
  });
});

const TABS = [
  { id: "planner", label: "Planner" },
  { id: "saved", label: "Saved", count: 2 },
  { id: "pva", label: "Plan vs actual" },
] as const;

describe("PlanTabs", () => {
  it("is a tablist with roving tabindex and linked panels", () => {
    const onChange = vi.fn();
    render(
      <>
        <PlanTabs tabs={[...TABS]} value="saved" onChange={onChange} />
        <PlanTabPanel tab="saved" active>
          saved body
        </PlanTabPanel>
        <PlanTabPanel tab="planner" active={false}>
          planner body
        </PlanTabPanel>
      </>,
    );
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((t) => t.getAttribute("tabindex"))).toEqual(["-1", "0", "-1"]);
    expect(tabs.map((t) => t.getAttribute("aria-selected"))).toEqual(["false", "true", "false"]);
    expect(screen.getByRole("tab", { name: /Saved/ })).toHaveTextContent("Saved 2");
    const panel = screen.getByRole("tabpanel");
    expect(panel).toHaveTextContent("saved body");
    expect(panel.getAttribute("aria-labelledby")).toBe(tabs[1].id);
    expect(tabs[1].getAttribute("aria-controls")).toBe(panel.id);
    expect(screen.queryByText("planner body")).toBeNull(); // inactive panels are not rendered
  });

  it("arrow keys select and focus the next tab; Home/End jump", () => {
    const seen: PlanTab[] = [];
    function Host() {
      const [tab, setTab] = usePlanTab(true);
      seen.push(tab);
      return <PlanTabs tabs={[...TABS]} value={tab} onChange={setTab} />;
    }
    render(
      <MemoryRouter initialEntries={["/plan"]}>
        <Host />
      </MemoryRouter>,
    );
    const list = screen.getByRole("tablist");
    fireEvent.keyDown(list, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: /Saved/ })).toHaveAttribute("aria-selected", "true");
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: /Saved/ }));
    fireEvent.keyDown(list, { key: "End" });
    expect(screen.getByRole("tab", { name: "Plan vs actual" })).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(list, { key: "ArrowRight" }); // wraps
    expect(screen.getByRole("tab", { name: "Planner" })).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(list, { key: "ArrowLeft" }); // wraps back
    expect(screen.getByRole("tab", { name: "Plan vs actual" })).toHaveAttribute("aria-selected", "true");
  });
});

describe("usePlanTab", () => {
  function Probe({ pva }: { pva: boolean }) {
    const [tab, setTab] = usePlanTab(pva);
    const loc = useLocation();
    return (
      <div>
        <span data-testid="tab">{tab}</span>
        <span data-testid="search">{loc.search}</span>
        <button onClick={() => setTab("saved")}>saved</button>
        <button onClick={() => setTab("planner")}>planner</button>
      </div>
    );
  }
  it("reads ?tab=, writes it, keeps other params, and drops it for the planner", () => {
    render(
      <MemoryRouter initialEntries={["/plan?trip=abc&tab=pva"]}>
        <Probe pva />
      </MemoryRouter>,
    );
    expect(screen.getByTestId("tab")).toHaveTextContent("pva");
    fireEvent.click(screen.getByText("saved"));
    expect(screen.getByTestId("tab")).toHaveTextContent("saved");
    expect(screen.getByTestId("search")).toHaveTextContent("?trip=abc&tab=saved");
    fireEvent.click(screen.getByText("planner"));
    expect(screen.getByTestId("tab")).toHaveTextContent("planner");
    expect(screen.getByTestId("search")).toHaveTextContent("?trip=abc");
  });
  it("falls back to the planner for pva without the Money shell", () => {
    render(
      <MemoryRouter initialEntries={["/plan?tab=pva"]}>
        <Probe pva={false} />
      </MemoryRouter>,
    );
    expect(screen.getByTestId("tab")).toHaveTextContent("planner");
  });
});
