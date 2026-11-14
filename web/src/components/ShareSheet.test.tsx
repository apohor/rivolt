import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("../lib/config", () => ({ ensureConfig: vi.fn().mockResolvedValue({ features: {} }) }));
vi.mock("../lib/shareCard", () => ({
  paintShareCard: vi.fn(),
  shareCardBlob: vi.fn().mockResolvedValue(new Blob(["x"], { type: "image/png" })),
}));

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
});
