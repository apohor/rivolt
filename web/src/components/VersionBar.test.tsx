import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { VersionBar } from "./VersionBar";
import { backend } from "../lib/api";

vi.mock("../lib/api", () => ({ backend: { health: vi.fn() } }));

const health = backend.health as unknown as ReturnType<typeof vi.fn>;
const setHidden = (hidden: boolean) =>
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
const reloadSpy = vi.fn();
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });

beforeEach(() => {
  vi.useFakeTimers();
  setHidden(false);
  reloadSpy.mockReset();
  Object.defineProperty(window, "location", { configurable: true, value: { hostname: "rivolt.dev", reload: reloadSpy } });
  health.mockReset();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("VersionBar", () => {
  it("shows the bar when the server version changes after load (prod: no auto-reload, even hidden)", async () => {
    health.mockResolvedValue({ ok: true, version: "v1", time: "" });
    render(<VersionBar />);
    await flush();
    expect(screen.queryByTestId("version-bar")).toBeNull();
    health.mockResolvedValue({ ok: true, version: "v2", time: "" });
    await act(async () => { vi.advanceTimersByTime(60_000); });
    await flush();
    expect(screen.getByTestId("version-bar")).toBeTruthy();
    setHidden(true);
    await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
    await flush();
    expect(reloadSpy).not.toHaveBeenCalled();
  });

  it("does not poll while the page is hidden", async () => {
    health.mockResolvedValue({ ok: true, version: "v1", time: "" });
    render(<VersionBar />);
    await flush();
    const n = health.mock.calls.length;
    setHidden(true);
    await act(async () => { vi.advanceTimersByTime(180_000); });
    expect(health.mock.calls.length).toBe(n);
  });

  it("preview reloads by itself once the tab is hidden", async () => {
    Object.defineProperty(window, "location", { configurable: true, value: { hostname: "preview.rivolt.dev", reload: reloadSpy } });
    health.mockResolvedValue({ ok: true, version: "v1", time: "" });
    render(<VersionBar />);
    await flush();
    health.mockResolvedValue({ ok: true, version: "v2", time: "" });
    setHidden(true);
    await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
    await flush();
    await act(async () => { vi.advanceTimersByTime(3100); });
    expect(reloadSpy).toHaveBeenCalled();
  });

  it("ignores failed health checks (offline shell)", async () => {
    health.mockRejectedValue(new Error("offline"));
    render(<VersionBar />);
    await flush();
    expect(screen.queryByTestId("version-bar")).toBeNull();
  });
});
