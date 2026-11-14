import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { Drive } from "../lib/api";

const flag = vi.hoisted(() => ({ on: true }));
const postcard = vi.hoisted(() => vi.fn());
vi.mock("../lib/config", () => ({ ensureConfig: vi.fn().mockResolvedValue({ features: {} }), useDesignPreview: () => flag.on }));
vi.mock("../lib/api", () => ({ backend: { drivePostcard: postcard } }));
vi.mock("../lib/demo", () => ({ inDemo: () => false }));
vi.mock("../lib/shareCard", () => ({
  paintShareCard: vi.fn(),
  shareCardBlob: vi.fn(),
  shareFileName: () => "t.png",
  buildMilestoneShareModel: vi.fn(),
  buildTripShareModel: vi.fn(() => ({})),
}));

import { ShareTripButton } from "./ShareButtons";

const drive = { ID: "d1", StartedAt: "2026-10-19T14:32:00Z" } as unknown as Drive;
const open = () => fireEvent.click(screen.getByRole("button", { name: "Share trip" }));

describe("ShareTripButton map toggle", () => {
  afterEach(() => {
    cleanup();
    postcard.mockReset();
    flag.on = true;
  });

  it("offers the toggle once the trimmed route loads", async () => {
    postcard.mockResolvedValue({ version: 1, distance_mi: 5, route: { segments: ["abc"] } });
    render(<ShareTripButton drive={drive} />);
    open();
    expect(await screen.findByLabelText(/Include map/)).toBeTruthy();
    expect(postcard).toHaveBeenCalledWith("d1", expect.anything());
  });

  it("has no toggle when the flag is off, and never asks the server", async () => {
    flag.on = false;
    render(<ShareTripButton drive={drive} />);
    open();
    await screen.findByRole("dialog");
    expect(screen.queryByLabelText(/Include map/)).toBeNull();
    expect(postcard).not.toHaveBeenCalled();
  });

  it("has no toggle when the drive has no usable route", async () => {
    postcard.mockRejectedValue(new Error("no_route"));
    render(<ShareTripButton drive={drive} />);
    open();
    await waitFor(() => expect(postcard).toHaveBeenCalled());
    expect(screen.queryByLabelText(/Include map/)).toBeNull();
  });
});
