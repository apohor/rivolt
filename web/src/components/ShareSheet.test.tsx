import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("../lib/config", () => ({ ensureConfig: vi.fn().mockResolvedValue({ features: {} }) }));
vi.mock("../lib/shareCard", () => ({
  paintShareCard: vi.fn(),
  shareMapBand: vi.fn(() => ({ w: 920, h: 200 })),
  shareCardBlob: vi.fn().mockResolvedValue(new Blob(["x"], { type: "image/png" })),
}));

const renderShareBasemap = vi.fn();
vi.mock("../map/shareBasemap", () => ({ renderShareBasemap: (...a: unknown[]) => renderShareBasemap(...a) }));

import { shareMapBand } from "../lib/shareCard";
import { ShareSheet } from "./ShareSheet";

describe("ShareSheet onShared", () => {
  afterEach(() => cleanup());

  it("fires once an image is saved, not when the sheet is only opened", async () => {
    Object.assign(URL, { createObjectURL: vi.fn(() => "blob:x"), revokeObjectURL: vi.fn() });
    const onShared = vi.fn();
    render(<ShareSheet build={() => ({}) as never} fileName="m.png" withCostToggle onShared={onShared} onClose={() => {}} />);
    expect(onShared).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Save image" }));
    await waitFor(() => expect(onShared).toHaveBeenCalledTimes(1));
  });

  it("shows Include map only when asked, off by default, and builds with it once ticked", () => {
    const build = vi.fn(() => ({}) as never);
    const { rerender } = render(<ShareSheet build={build} fileName="t.png" withCostToggle onClose={() => {}} />);
    expect(screen.queryByLabelText(/Include map/)).toBeNull();
    rerender(<ShareSheet build={build} fileName="t.png" withCostToggle withMapToggle onClose={() => {}} />);
    const box = screen.getByLabelText(/Include map/) as HTMLInputElement;
    expect(box.checked).toBe(false);
    expect(build).toHaveBeenLastCalledWith(false, false, false);
    fireEvent.click(box);
    expect(build).toHaveBeenLastCalledWith(false, false, true);
  });

  it("never asks for the map when the toggle is not shown", () => {
    const build = vi.fn(() => ({}) as never);
    render(<ShareSheet build={build} fileName="t.png" withCostToggle onClose={() => {}} />);
    expect(build).toHaveBeenLastCalledWith(false, false, false);
  });

  it("says the map is unavailable, only once it was asked for and could not be drawn", async () => {
    renderShareBasemap.mockResolvedValue(null);
    const build = vi.fn((_c: boolean, _o: boolean, map: boolean) => (map ? { map: { segments: ["a"] } } : {}) as never);
    render(<ShareSheet build={build} fileName="t.png" withCostToggle withMapToggle onClose={() => {}} />);
    expect(screen.queryByText(/Map unavailable/)).toBeNull();
    fireEvent.click(screen.getByLabelText(/Include map/));
    expect(await screen.findByText("Map unavailable. The card shows the route as a line.")).toBeTruthy();
    fireEvent.click(screen.getByLabelText(/Include map/));
    await waitFor(() => expect(screen.queryByText(/Map unavailable/)).toBeNull());
  });

  it("shows no note when the map is drawn", async () => {
    renderShareBasemap.mockResolvedValue({});
    const build = vi.fn((_c: boolean, _o: boolean, map: boolean) => (map ? { map: { segments: ["b"] } } : {}) as never);
    render(<ShareSheet build={build} fileName="t.png" withCostToggle withMapToggle onClose={() => {}} />);
    fireEvent.click(screen.getByLabelText(/Include map/));
    await waitFor(() => expect(renderShareBasemap).toHaveBeenCalled());
    expect(screen.queryByText(/Map unavailable/)).toBeNull();
  });

  it("shows no note when the card has no room for a map", async () => {
    renderShareBasemap.mockClear();
    renderShareBasemap.mockResolvedValue(null);
    vi.mocked(shareMapBand).mockReturnValue(null);
    const build = vi.fn((_c: boolean, _o: boolean, map: boolean) => (map ? { map: { segments: ["c"] } } : {}) as never);
    render(<ShareSheet build={build} fileName="t.png" withCostToggle withMapToggle onClose={() => {}} />);
    fireEvent.click(screen.getByLabelText(/Include map/));
    await waitFor(() => expect(build).toHaveBeenLastCalledWith(false, false, true));
    expect(renderShareBasemap).not.toHaveBeenCalled();
    expect(screen.queryByText(/Map unavailable/)).toBeNull();
    vi.mocked(shareMapBand).mockReturnValue({ w: 920, h: 200 });
  });
});
