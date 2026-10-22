import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HomeLocationPanel, homeMoved, parseHomeCoordinates } from "./SettingsPage";
import { UseAsHome } from "./ChargeDetailPage";
import { MemoryRouter } from "react-router-dom";

const OLD = { set: true, latitude: 40, longitude: -100, label: "Home", source: "city", previous_set: false, address_search: false };
const NEW = { set: true, latitude: 41, longitude: -100, label: "Home", source: "coordinates", previous_set: true, address_search: false };

let homeGet: object = OLD;
const bodies: { url: string; body: string }[] = [];

function mockFetch(dry: object, flag = true) {
  const calls: string[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? "GET"} ${url}`);
    bodies.push({ url, body: String(init?.body ?? "") });
    const j = (body: unknown) =>
      ({ ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify(body) }) as unknown as Response;
    if (url.startsWith("/api/ledger/enabled")) {
      return flag
        ? j({ enabled: true })
        : ({ ok: false, status: 404, headers: { get: () => "application/json" }, text: async () => "{}" } as unknown as Response);
    }
    if (url.startsWith("/api/settings/home-location/search")) return j([{ name: "1 Main Street", latitude: 41, longitude: -100, admin1: "Texas", country: "United States" }]);
    if (url.startsWith("/api/settings/home-location/restore")) return j(OLD);
    if (url.startsWith("/api/settings/home-location")) return j(init?.method === "PUT" ? NEW : homeGet);
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

// Exact coordinates: the way every install has (the address field needs Photon).
async function pickNewHome() {
  fireEvent.change(await screen.findByLabelText("Latitude"), { target: { value: "41" } });
  fireEvent.change(screen.getByLabelText("Longitude"), { target: { value: "-100" } });
  fireEvent.click(screen.getByRole("button", { name: "Save coordinates" }));
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

describe("precise home (#906)", () => {
  beforeEach(() => {
    _resetMoneyTabFlagForTests();
    homeGet = OLD;
    bodies.length = 0;
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("saves typed coordinates as source coordinates, and a pasted pair fills both boxes", async () => {
    const calls = mockFetch({ dry_run: true, changed: 0, to_home: 0, to_away: 0, unchanged: 1, skipped_user_priced: 0 });
    renderPanel();
    fireEvent.change(await screen.findByLabelText("Latitude"), { target: { value: "32.776665, -96.796989" } });
    expect((screen.getByLabelText("Longitude") as HTMLInputElement).value).toBe("-96.796989");
    fireEvent.click(screen.getByRole("button", { name: "Save coordinates" }));
    await vi.waitFor(() => expect(calls.some((c) => c === "PUT /api/settings/home-location")).toBe(true));
    const put = JSON.parse(bodies.find((b) => b.url === "/api/settings/home-location" && b.body.includes("latitude"))!.body);
    expect(put).toMatchObject({ set: true, latitude: 32.776665, longitude: -96.796989, source: "coordinates" });
  });

  it("refuses coordinates out of range before sending anything", async () => {
    const calls = mockFetch({ dry_run: true, changed: 0 });
    renderPanel();
    fireEvent.change(await screen.findByLabelText("Latitude"), { target: { value: "91" } });
    fireEvent.change(screen.getByLabelText("Longitude"), { target: { value: "10" } });
    expect(screen.getByText("Latitude must be between -90 and 90.")).toBeInTheDocument();
    expect((screen.getByRole("button", { name: "Save coordinates" }) as HTMLButtonElement).disabled).toBe(true);
    expect(calls.some((c) => c.startsWith("PUT"))).toBe(false);
  });

  it("hides the address field without Photon, and searches by POST (the address in the body) with it", async () => {
    const calls = mockFetch({ dry_run: true, changed: 0 });
    renderPanel();
    await screen.findByLabelText("Latitude");
    expect(screen.queryByLabelText("Home street address")).toBeNull();
    cleanup();
    homeGet = { ...OLD, address_search: true };
    renderPanel();
    fireEvent.change(await screen.findByLabelText("Home street address"), { target: { value: "1 Main St" } });
    fireEvent.click(await screen.findByText("1 Main Street", {}, { timeout: 3000 }));
    expect(calls.some((c) => c === "POST /api/settings/home-location/search")).toBe(true);
    expect(calls.some((c) => c.includes("?q=") || c.includes("/api/geocode"))).toBe(false);
    await vi.waitFor(() => expect(bodies.some((b) => b.body.includes('"source":"address"'))).toBe(true));
  });

  it("offers to restore the previous home", async () => {
    const calls = mockFetch({ dry_run: true, changed: 0 });
    homeGet = { ...NEW };
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Restore the previous home" }));
    await vi.waitFor(() => expect(calls.some((c) => c === "POST /api/settings/home-location/restore")).toBe(true));
  });

  it("uses a charge's location as home only after a confirmation, then asks wrong-or-moved", async () => {
    const calls = mockFetch({ dry_run: true, changed: 2, to_home: 2, to_away: 0, unchanged: 5, skipped_user_priced: 0 });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MemoryRouter>
        <QueryClientProvider client={qc}>
          <UseAsHome lat={32.776665} lon={-96.796989} />
        </QueryClientProvider>
      </MemoryRouter>,
    );
    // The page has read the money flag before anyone taps (the re-check prompt ships with it).
    await vi.waitFor(() => expect(calls.some((c) => c.startsWith("GET /api/ledger/enabled"))).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    fireEvent.click(screen.getByRole("button", { name: "Use this charge's location as home" }));
    expect(calls.some((c) => c.startsWith("PUT"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Set as home" }));
    expect(await screen.findByText("Was the old location wrong, or did you move?")).toBeInTheDocument();
    expect(JSON.parse(bodies.find((b) => b.body.includes("latitude"))!.body)).toMatchObject({ latitude: 32.776665, longitude: -96.796989, source: "charge" });
    expect(calls.some((c) => c === "POST /api/charges/restamp-place")).toBe(false);
  });

  it("checks coordinates as the server does", () => {
    expect(parseHomeCoordinates("32.7", "-96.8")).toEqual({ ok: true, lat: 32.7, lon: -96.8 });
    expect(parseHomeCoordinates("-90", "180").ok).toBe(true);
    expect(parseHomeCoordinates("", "1").ok).toBe(false);
    expect(parseHomeCoordinates("abc", "1").ok).toBe(false);
    expect(parseHomeCoordinates("10", "181").ok).toBe(false);
  });
});

describe("homeMoved", () => {
  it("ignores a re-saved pin and flags a real move", () => {
    expect(homeMoved(OLD, { latitude: 40.0001, longitude: -100 })).toBe(false);
    expect(homeMoved(OLD, NEW)).toBe(true);
  });
});
