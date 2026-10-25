import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SignedInDevices } from "./SignedInDevices";

const row = (id: string, o: object = {}) => ({ id, kind: "web", device_label: `Chrome on Mac ${id}`, created_at: "2026-10-01T10:00:00Z", last_seen_at: "2026-10-24T10:00:00Z", expires_at: "2027-01-01T00:00:00Z", current: false, ...o });

function stub(handler: (url: string) => { sessions: unknown[]; total?: number; hidden_web?: number }) {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      urls.push(url);
      const body = handler(url);
      return { ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify(body), json: async () => body } as unknown as Response;
    }),
  );
  return urls;
}

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <SignedInDevices />
    </QueryClientProvider>,
  );
}

describe("SignedInDevices with the server's cut", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("asks for the recent ones, says how many older browsers are not shown, and Show all loads the rest", async () => {
    const urls = stub((url) =>
      url.includes("recent=10")
        ? { sessions: [row("a", { current: true }), row("b"), row("app", { kind: "app", device_label: "iPhone" })], total: 14, hidden_web: 11 }
        : { sessions: [row("a", { current: true }), row("b"), row("c"), row("app", { kind: "app", device_label: "iPhone" })], total: 14, hidden_web: 0 },
    );
    renderCard();
    expect(await screen.findByText("11 older browsers not shown")).toBeTruthy();
    expect(urls.some((u) => u.includes("/api/auth/sessions?recent=10"))).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Show all" }));
    expect(await screen.findByText(/Chrome on Mac c/)).toBeTruthy();
    expect(document.querySelector("[data-devices-hidden]")).toBeNull();
    expect(urls.some((u) => u.endsWith("/api/auth/sessions"))).toBe(true);
  });

  it("says nothing about older browsers when none are hidden", async () => {
    stub(() => ({ sessions: [row("a", { current: true }), row("b")], total: 2, hidden_web: 0 }));
    renderCard();
    await screen.findByText(/Chrome on Mac b/);
    expect(document.querySelector("[data-devices-hidden]")).toBeNull();
  });

  it("counts the hidden browsers in what Sign out everywhere else will sign out", async () => {
    stub(() => ({ sessions: [row("a", { current: true }), row("b")], total: 6, hidden_web: 4 }));
    renderCard();
    await screen.findByText("4 older browsers not shown");
    fireEvent.click(screen.getByRole("button", { name: "Sign out everywhere else" }));
    expect(await screen.findByRole("button", { name: "Yes, sign out 5 other devices" })).toBeTruthy();
  });

  it("says one older browser in the singular", async () => {
    stub(() => ({ sessions: [row("a", { current: true })], total: 2, hidden_web: 1 }));
    renderCard();
    expect(await screen.findByText("1 older browser not shown")).toBeTruthy();
  });
});
