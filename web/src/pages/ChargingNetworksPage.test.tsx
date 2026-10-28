import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import ChargingNetworksPage from "./ChargingNetworksPage";
import { stubFetchRoutes } from "../test/fetchRoutes";
import { PRICE_AT_CHARGER, priceCell, safeTrackerUrl } from "../lib/chargingNetworks";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const OBS = {
  low_per_kwh: 0.43, high_per_kwh: 0.5, month: "2026-10", source: "DCFC Tracker", url: "https://dcfctracker.com/report/ea",
  label: "Typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ.",
};
const data = (extra: object = {}) => ({
  networks: [
    {
      slug: "ea", name: "Electrify America",
      plans: [{ plan: "Pass+", terms: "Lower per-kWh price for a monthly fee.", source_url: "https://www.electrifyamerica.com/pricing/", checked: "2026-10-20", label: "As listed by Electrify America; may be out of date; last checked 2026-10-20." }],
      observed: OBS, price: OBS.label,
    },
    { slug: "tesla", name: "Tesla Supercharger", plans: null, observed: null, price: PRICE_AT_CHARGER },
  ],
  observed_terms_url: "https://dcfctracker.com/terms",
  observed_source: "DCFC Tracker",
  ...extra,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ChargingNetworksPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("/charging-networks", () => {
  it("lists plans with their check date and source, and typical prices with the server's label", async () => {
    stubFetchRoutes({ "/api/charging-networks": { body: data() } });
    renderPage();
    expect(await screen.findByText("Pass+")).toBeInTheDocument();
    expect(screen.getByText(/As listed by Electrify America; may be out of date; last checked 2026-10-20\./)).toBeInTheDocument();
    const plans = within(document.querySelector("[data-networks-plans]") as HTMLElement);
    expect(plans.getByRole("link", { name: "Source" })).toHaveAttribute("href", "https://www.electrifyamerica.com/pricing/");
    const prices = within(document.querySelector("[data-networks-prices]") as HTMLElement);
    expect(prices.getByText(/Typical price seen by DCFC Tracker, Oct 2026\. The price at the charger may differ\./)).toBeInTheDocument();
    // A network with no figure reads the plain line, never a number.
    expect(prices.getByText(PRICE_AT_CHARGER)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /DCFC Tracker \(terms\)/ })).toHaveAttribute("href", "https://dcfctracker.com/terms");
    expect(screen.getByText(/Unofficial, not affiliated with Rivian or with any charging network/)).toBeInTheDocument();
  });

  it("reads 'Price shown at the charger' throughout when the tracker's terms are not linked (DA's condition)", async () => {
    stubFetchRoutes({ "/api/charging-networks": { body: data({ observed_terms_url: "" }) } });
    renderPage();
    await screen.findByText("Pass+");
    const prices = within(document.querySelector("[data-networks-prices]") as HTMLElement);
    expect(prices.getAllByText(PRICE_AT_CHARGER)).toHaveLength(2);
    expect(prices.queryByText(/Typical price seen/)).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /DCFC Tracker \(terms\)/ })).not.toBeInTheDocument();
  });

  it("says so when the table can't be loaded", async () => {
    stubFetchRoutes({ "/api/charging-networks": { status: 500, body: { error: "x" } } });
    renderPage();
    expect(await screen.findByText(/The table is not available right now/)).toBeInTheDocument();
  });
});

describe("links to the tracker", () => {
  it("only an https page on the tracker's own site", () => {
    expect(safeTrackerUrl("https://dcfctracker.com/terms")).toBe("https://dcfctracker.com/terms");
    expect(safeTrackerUrl("https://www.dcfctracker.com/x")).toBe("https://www.dcfctracker.com/x");
    for (const bad of ["http://dcfctracker.com/x", "https://dcfctracker.com.evil.example/x", "https://u:p@dcfctracker.com/x", "", undefined, "nope"]) {
      expect(safeTrackerUrl(bad as string | undefined)).toBeNull();
    }
  });
  it("a price cell falls back to the plain line without terms or a figure", () => {
    const row = { slug: "x", name: "X", plans: null, observed: OBS, price: OBS.label };
    expect(priceCell(row, true).text).toBe(OBS.label);
    expect(priceCell(row, true).href).toBe("https://dcfctracker.com/report/ea");
    expect(priceCell(row, false)).toEqual({ text: PRICE_AT_CHARGER, href: null });
    expect(priceCell({ ...row, observed: null, price: PRICE_AT_CHARGER }, true)).toEqual({ text: PRICE_AT_CHARGER, href: null });
  });
});
