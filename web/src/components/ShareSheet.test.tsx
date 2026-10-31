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
});
