import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { AvoidToggles } from "./AvoidToggles";
import { noRoutePrefs, type RoutePrefs } from "./routePrefs";

afterEach(cleanup);

function Harness({ initial = noRoutePrefs, onChange = () => {} }: { initial?: RoutePrefs; onChange?: (p: RoutePrefs) => void }) {
  const [v, setV] = useState(initial);
  return (
    <AvoidToggles
      value={v}
      onChange={(n) => {
        setV(n);
        onChange(n);
      }}
    />
  );
}

describe("AvoidToggles", () => {
  it("is a labelled group of three checkboxes, all off by default", () => {
    render(<Harness />);
    expect(screen.getByRole("group", { name: "Avoid" })).toBeInTheDocument();
    for (const name of ["Tolls", "Ferries", "Highways"]) {
      expect(screen.getByRole("checkbox", { name })).not.toBeChecked();
    }
  });

  it("carries growth's helper text and ties it to the group", () => {
    render(<Harness />);
    const group = screen.getByRole("group", { name: "Avoid" });
    const text = "Applies to every day of this trip and is saved with it. Avoiding highways usually adds time and may add charging stops.";
    expect(screen.getByText(text)).toBeInTheDocument();
    expect(group.getAttribute("aria-describedby")).toBe(screen.getByText(text).id);
  });

  it("reports each change with the others untouched", () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    fireEvent.click(screen.getByRole("checkbox", { name: "Tolls" }));
    expect(onChange).toHaveBeenLastCalledWith({ avoidTolls: true, avoidFerries: false, avoidHighways: false });
    fireEvent.click(screen.getByRole("checkbox", { name: "Highways" }));
    expect(onChange).toHaveBeenLastCalledWith({ avoidTolls: true, avoidFerries: false, avoidHighways: true });
    fireEvent.click(screen.getByRole("checkbox", { name: "Tolls" }));
    expect(onChange).toHaveBeenLastCalledWith({ avoidTolls: false, avoidFerries: false, avoidHighways: true });
    expect(screen.getByRole("checkbox", { name: "Tolls" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Highways" })).toBeChecked();
  });

  it("shows what a saved trip had", () => {
    render(<Harness initial={{ avoidTolls: false, avoidFerries: true, avoidHighways: false }} />);
    expect(screen.getByRole("checkbox", { name: "Ferries" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Tolls" })).not.toBeChecked();
  });
});
