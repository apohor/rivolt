import { afterEach, describe, expect, it } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { PlanSelectionProvider, useSelectedStop, type SelectedStop } from "./selection";

afterEach(cleanup);

// A consumer that exposes its hook result to the test.
function Probe({ id, routeKey, shared, out }: { id: string; routeKey: unknown; shared?: boolean; out: Record<string, SelectedStop> }) {
  const sel = useSelectedStop(routeKey, { shared });
  out[id] = sel;
  return <span data-testid={id}>{sel.selected === null ? "none" : sel.selected}</span>;
}

const ROUTE_A = { name: "a" };
const ROUTE_B = { name: "b" };

describe("useSelectedStop", () => {
  it("shared consumers under one provider stay in step (itinerary and map)", () => {
    const out: Record<string, SelectedStop> = {};
    render(
      <PlanSelectionProvider routeKey={ROUTE_A}>
        <Probe id="itinerary" routeKey={ROUTE_A} shared out={out} />
        <Probe id="map" routeKey={ROUTE_A} shared out={out} />
      </PlanSelectionProvider>,
    );
    act(() => out.itinerary.select(3));
    expect(screen.getByTestId("map")).toHaveTextContent("3");
    act(() => out.map.select(1));
    expect(screen.getByTestId("itinerary")).toHaveTextContent("1");
    act(() => out.map.clear());
    expect(screen.getByTestId("itinerary")).toHaveTextContent("none");
  });

  it("toggle selects, then deselects the same stop, and moves to another", () => {
    const out: Record<string, SelectedStop> = {};
    render(
      <PlanSelectionProvider routeKey={ROUTE_A}>
        <Probe id="a" routeKey={ROUTE_A} shared out={out} />
      </PlanSelectionProvider>,
    );
    act(() => out.a.toggle(2));
    expect(screen.getByTestId("a")).toHaveTextContent("2");
    act(() => out.a.toggle(2));
    expect(screen.getByTestId("a")).toHaveTextContent("none");
    act(() => out.a.toggle(2));
    act(() => out.a.toggle(0)); // origin is index 0: still a real selection
    expect(screen.getByTestId("a")).toHaveTextContent("0");
  });

  it("a new route clears the selection (provider)", () => {
    const out: Record<string, SelectedStop> = {};
    const { rerender } = render(
      <PlanSelectionProvider routeKey={ROUTE_A}>
        <Probe id="a" routeKey={ROUTE_A} shared out={out} />
      </PlanSelectionProvider>,
    );
    act(() => out.a.select(4));
    expect(screen.getByTestId("a")).toHaveTextContent("4");
    rerender(
      <PlanSelectionProvider routeKey={ROUTE_B}>
        <Probe id="a" routeKey={ROUTE_B} shared out={out} />
      </PlanSelectionProvider>,
    );
    expect(screen.getByTestId("a")).toHaveTextContent("none");
    // and the same key again does not clear
    act(() => out.a.select(1));
    rerender(
      <PlanSelectionProvider routeKey={ROUTE_B}>
        <Probe id="a" routeKey={ROUTE_B} shared out={out} />
      </PlanSelectionProvider>,
    );
    expect(screen.getByTestId("a")).toHaveTextContent("1");
  });

  it("without a provider, or when not shared, each consumer keeps its own selection", () => {
    const out: Record<string, SelectedStop> = {};
    render(
      <>
        <Probe id="alone1" routeKey={ROUTE_A} shared out={out} />
        <Probe id="alone2" routeKey={ROUTE_A} shared out={out} />
        <PlanSelectionProvider routeKey={ROUTE_A}>
          <Probe id="shared" routeKey={ROUTE_A} shared out={out} />
          <Probe id="classic" routeKey={ROUTE_A} out={out} />
        </PlanSelectionProvider>
      </>,
    );
    act(() => out.alone1.select(5));
    expect(screen.getByTestId("alone2")).toHaveTextContent("none"); // no provider: independent
    act(() => out.shared.select(2));
    expect(screen.getByTestId("classic")).toHaveTextContent("none"); // alternatives keep their own
    act(() => out.classic.select(7));
    expect(screen.getByTestId("shared")).toHaveTextContent("2");
    expect(screen.getByTestId("classic")).toHaveTextContent("7");
  });

  it("an own selection resets when its route changes", () => {
    const out: Record<string, SelectedStop> = {};
    const { rerender } = render(<Probe id="c" routeKey={ROUTE_A} out={out} />);
    act(() => out.c.select(3));
    expect(screen.getByTestId("c")).toHaveTextContent("3");
    rerender(<Probe id="c" routeKey={ROUTE_B} out={out} />);
    expect(screen.getByTestId("c")).toHaveTextContent("none");
  });
});
