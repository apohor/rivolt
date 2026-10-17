import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import MoneyPage from "./MoneyPage";
import { currentMonth, ledgerMonthFixture } from "../lib/ledger";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/money" element={<MoneyPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("MoneyPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "location", {
      value: { pathname: "/money", search: "", assign: vi.fn() },
      writable: true,
    });
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("renders the server's statement with provenance and quality states", async () => {
    const m = ledgerMonthFixture(currentMonth());
    stubFetchRoutes({
      "/api/vehicles": { body: [] },
      "/api/ledger/monthly": { body: m },
      "/api/ledger/summary": { body: { version: 1, tz: m.tz, currency: "USD", months: [] } },
    });
    renderAt("/money");

    const statement = await screen.findByRole("region", { name: "Statement" });
    // Hero is the server's total, formatted; not re-summed.
    expect(statement).toHaveTextContent(
      new Intl.NumberFormat(undefined, { style: "currency", currency: "USD" }).format(m.spend.total),
    );
    expect(statement).toHaveTextContent("Month to date");
    expect(statement).toHaveTextContent(/Estimate pending:\s*2 fast-charging sessions/);
    expect(statement).toHaveTextContent(/Likely wrong:\s*1 session away from home/);
    expect(statement).toHaveTextContent(/Not included: 2 unpriced sessions/);
    expect(statement).toHaveTextContent(/% estimated/);
    // Not sample data: the API answered.
    expect(screen.queryByText(/Sample data/)).toBeNull();

    // Any receipt with an unpriced or pending session → Add price (the
    // mixed DCFC stop's pending part outranks its likely-wrong tail).
    expect(screen.getAllByText("Add price →").length).toBe(4);
    expect(screen.queryByText("Fix price →")).toBeNull();
    expect(screen.getAllByText("Pending").length).toBeGreaterThan(0);
    expect(screen.getByText("Free")).toBeInTheDocument();
    expect(screen.getByText(/Estimate · Electrify America/)).toBeInTheDocument();
    // Two server-owned splits: where (by_place) and how (by_kind).
    expect(screen.getByRole("img", { name: /^Charging: Home .*, Away .*, Fast / })).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /^Where:/ })).toBeNull();
  });

  it("filters receipts and expands folded top-ups", async () => {
    const m = ledgerMonthFixture(currentMonth());
    stubFetchRoutes({
      "/api/vehicles": { body: [] },
      "/api/ledger/monthly": { body: m },
      "/api/ledger/summary": { body: { version: 1, tz: m.tz, currency: "USD", months: [] } },
    });
    renderAt("/money");
    await screen.findByRole("region", { name: "Statement" });

    const filters = screen.getByRole("group", { name: "Filter receipts" });
    fireEvent.click(within(filters).getByRole("button", { name: "Fast" }));
    const list = screen.getAllByRole("listitem").filter((li) => li.textContent?.includes("kW peak"));
    expect(list.length).toBe(4);
    expect(list.every((li) => li.textContent?.includes("Fast"))).toBe(true);

    fireEvent.click(within(filters).getByRole("button", { name: "Home" }));
    const toggle = screen.getByRole("button", { name: /\+ 9 top-ups\/fragments/ });
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
  });

  it("labels the fixture as sample data when the ledger isn't deployed", async () => {
    stubFetchRoutes({ "/api/vehicles": { body: [] } }); // ledger routes 404
    renderAt("/money");
    expect(await screen.findByText(/Sample data\./)).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Statement" })).toBeInTheDocument();
  });

  it("shows an error instead of fake numbers on a server failure", async () => {
    stubFetchRoutes({
      "/api/vehicles": { body: [] },
      "/api/ledger/monthly": { status: 500, body: "db down" },
      "/api/ledger/summary": { status: 500, body: "db down" },
    });
    renderAt("/money");
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't load this month");
    expect(screen.queryByRole("region", { name: "Statement" })).toBeNull();
  });

  it("renders an empty month plainly", async () => {
    const m = ledgerMonthFixture(currentMonth());
    const empty = {
      ...m,
      receipts: [],
      spend: { ...m.spend, total: 0, unpriced_sessions: 0, priced_sessions: 0, unpriced_kwh: 0 },
      data_quality: { ...m.data_quality, superseded_excluded: 0, home_rate_on_dcfc: 0, home_rate_away: 0, cost_source_inferred: false },
      energy: { ...m.energy, null_energy_sessions: 0 },
    };
    stubFetchRoutes({
      "/api/vehicles": { body: [] },
      "/api/ledger/monthly": { body: empty },
      "/api/ledger/summary": { body: { version: 1, tz: m.tz, currency: "USD", months: [] } },
    });
    renderAt("/money");
    expect(await screen.findByText(/No charging recorded in/)).toBeInTheDocument();
  });

  it("falls back to the two bars when by_place_kind is absent", async () => {
    const m = ledgerMonthFixture(currentMonth());
    delete (m.energy as { by_place_kind?: unknown }).by_place_kind;
    stubFetchRoutes({
      "/api/vehicles": { body: [] },
      "/api/ledger/monthly": { body: m },
      "/api/ledger/summary": { body: { version: 1, tz: m.tz, currency: "USD", months: [] } },
    });
    renderAt("/money");
    await screen.findByRole("region", { name: "Statement" });
    expect(screen.getByRole("img", { name: /^Where: Home .*, Away / })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /^How: AC .*, Fast / })).toBeInTheDocument();
  });

  it("To confirm lists every open charge across months and agrees with the all-time badge", async () => {
    const m = ledgerMonthFixture(currentMonth());
    const ch = (id: string, daysAgo: number, state: string | null) => ({
      ID: id,
      StartedAt: new Date(Date.now() - daysAgo * 86_400_000).toISOString(),
      EnergyAddedKWh: 40,
      Cost: 16,
      PricePerKWh: 0.4,
      Currency: "USD",
      network_state: state,
      network_evidence: { candidates: [{ network: "Tesla Supercharger", dist_m: 30 }] },
    });
    const fetchMock = stubFetchRoutes({
      "/api/vehicles": { body: [] },
      "/api/ledger/monthly": { body: m },
      "/api/ledger/summary": { body: { version: 1, tz: m.tz, currency: "USD", months: [], to_confirm_count: 3 } },
      "/api/charges": {
        body: [ch("a", 1, "estimated"), ch("b", 40, "needs_confirmation"), ch("c", 75, "estimated"), ch("d", 5, "confirmed")],
      },
    });
    renderAt("/money?filter=confirm");

    const list = await screen.findByTestId("to-confirm-list");
    // Same number as the badge: confirmed charges are not listed.
    expect(within(list).getAllByRole("link")).toHaveLength(3);
    expect(await screen.findByRole("button", { name: /3 charges to confirm/ })).toBeInTheDocument();
    // The list must ask for enough history: the default of 50 would drop older ones.
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => /\/api\/charges\?limit=5000/.test(u))).toBe(true);
  });

  it("keeps the legacy 'older version' wording when to_confirm_count is 0 and no row has a network_state", async () => {
    const m = ledgerMonthFixture(currentMonth());
    stubFetchRoutes({
      "/api/vehicles": { body: [] },
      "/api/ledger/monthly": { body: m },
      "/api/ledger/summary": { body: { version: 1, tz: m.tz, currency: "USD", months: [], to_confirm_count: 0 } },
    });
    renderAt("/money");
    const statement = await screen.findByRole("region", { name: "Statement" });
    expect(statement).toHaveTextContent(/Estimate pending:\s*2 fast-charging sessions/);
    expect(statement).not.toHaveTextContent(/Couldn't identify the network/);
  });

  it("switches to the auto-network wording once a row carries network_state", async () => {
    const m = ledgerMonthFixture(currentMonth());
    m.receipts[0].sessions[0].network_state = "confirmed";
    stubFetchRoutes({
      "/api/vehicles": { body: [] },
      "/api/ledger/monthly": { body: m },
      "/api/ledger/summary": { body: { version: 1, tz: m.tz, currency: "USD", months: [], to_confirm_count: 0 } },
    });
    renderAt("/money");
    const statement = await screen.findByRole("region", { name: "Statement" });
    expect(statement).not.toHaveTextContent(/older version/);
  });
});
