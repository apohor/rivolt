import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HomeLocationPanel, homeMoved } from "./SettingsPage";

const OLD = { set: true, latitude: 40, longitude: -100, label: "Home" };
const NEW = { set: true, latitude: 41, longitude: -100, label: "Home" };

function mockFetch(dry: object, flag = true) {
  const calls: string[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? "GET"} ${url}`);
    const j = (body: unknown) =>
      ({ ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify(body) }) as unknown as Response;
    if (url.startsWith("/api/ledger/enabled")) {
      return flag
        ? j({ enabled: true })
        : ({ ok: false, status: 404, headers: { get: () => "application/json" }, text: async () => "{}" } as unknown as Response);
    }
    if (url.startsWith("/api/settings/home-location")) return j(init?.method === "PUT" ? NEW : OLD);
    if (url.startsWith("/api/geocode")) return j([{ name: "Elsewhere", latitude: 41, longitude: -100, admin1: "", country: "" }]);
    if (url.startsWith("/api/charges/restamp-place/undo")) return j({ restamp_id: "r1", restored_place: 4, unpriced: 2 });
    if (url.startsWith("/api/charges/restamp-place")) {
      return j(url.includes("dry_run=1") ? dry : { dry_run: false, changed: 4, to_home: 3, to_away: 1, unchanged: 9, skipped_user_priced: 0, would_price_at_home_rate: 2, total_cost_delta: 5.5, currency: "USD", restamp_id: "r1" });
    }
    return { ok: false, status: 404, headers: { get: () => "application/json" }, text: async () => "{}" } as unknown as Response;
  });
  vi.stubGlobal("fetch", fn);
  return calls;
}

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <HomeLocationPanel />
    </QueryClientProvider>,
  );
}

async function pickNewHome() {
  const input = await screen.findByPlaceholderText(/different home/);
  fireEvent.change(input, { target: { value: "Elsewhere" } });
  fireEvent.click(await screen.findByText("Elsewhere", {}, { timeout: 3000 }));
}

import { _resetMoneyTabFlagForTests } from "../lib/ledger";

describe("home change prompt", () => {
  beforeEach(() => _resetMoneyTabFlagForTests());

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("asks wrong-or-moved after changing home, and re-stamps only on 'wrong'", async () => {
    const calls = mockFetch({ dry_run: true, changed: 4, to_home: 3, to_away: 1, unchanged: 9, skipped_user_priced: 0 });
    renderPanel();
    await pickNewHome();
    expect(await screen.findByText("Was the old location wrong, or did you move?")).toBeInTheDocument();
    expect(calls.some((c) => c.includes("restamp-place?dry_run=1"))).toBe(true);
    expect(calls.some((c) => c === "POST /api/charges/restamp-place")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "It was wrong, re-check" }));
    expect(await screen.findByText(/Updated 4 charges/)).toBeInTheDocument();
    expect(calls.some((c) => c === "POST /api/charges/restamp-place")).toBe(true);
  });

  it("'moved' keeps history: no real re-stamp call", async () => {
    const calls = mockFetch({ dry_run: true, changed: 4, to_home: 3, to_away: 1, unchanged: 9, skipped_user_priced: 0 });
    renderPanel();
    await pickNewHome();
    fireEvent.click(await screen.findByRole("button", { name: "I moved, keep history" }));
    expect(calls.some((c) => c === "POST /api/charges/restamp-place")).toBe(false);
    expect(screen.queryByText("Was the old location wrong, or did you move?")).toBeNull();
  });

  it("doesn't ask when nothing would change", async () => {
    mockFetch({ dry_run: true, changed: 0, to_home: 0, to_away: 0, unchanged: 13, skipped_user_priced: 0 });
    renderPanel();
    await pickNewHome();
    await vi.waitFor(() => expect(screen.queryByText("Was the old location wrong, or did you move?")).toBeNull());
  });
});

describe("pricing and undo (A15b)", () => {
  beforeEach(() => _resetMoneyTabFlagForTests());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });
  it("shows what will be priced, then offers Undo after applying", async () => {
    const calls = mockFetch({
      dry_run: true, changed: 4, to_home: 3, to_away: 1, unchanged: 9, skipped_user_priced: 0,
      would_price_at_home_rate: 2, total_cost_delta: 5.5, currency: "USD", restamp_id: null,
    });
    renderPanel();
    await pickNewHome();
    expect(await screen.findByText(/2 unpriced home charges will be priced at your home rate \(\+5\.50 USD\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "It was wrong, re-check" }));
    expect(await screen.findByText(/2 priced at your home rate/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(await screen.findByText(/Restored 4 charges; 2 unpriced again/)).toBeInTheDocument();
    expect(calls.some((c) => c.includes("restamp-place/undo?restamp_id=r1"))).toBe(true);
  });
});

describe("flag gate", () => {
  beforeEach(() => _resetMoneyTabFlagForTests());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });
  it("never prompts or re-stamps with money_tab_enabled off", async () => {
    const calls = mockFetch({ dry_run: true, changed: 4, to_home: 3, to_away: 1, unchanged: 9, skipped_user_priced: 0 }, false);
    renderPanel();
    await pickNewHome();
    await vi.waitFor(() => expect(calls.some((c) => c.startsWith("PUT /api/settings/home-location"))).toBe(true));
    await new Promise((r) => setTimeout(r, 200));
    expect(calls.some((c) => c.includes("restamp-place"))).toBe(false);
    expect(screen.queryByText("Was the old location wrong, or did you move?")).toBeNull();
  });
});

describe("homeMoved", () => {
  it("ignores a re-saved pin and flags a real move", () => {
    expect(homeMoved(OLD, { latitude: 40.0001, longitude: -100 })).toBe(false);
    expect(homeMoved(OLD, NEW)).toBe(true);
  });
});
