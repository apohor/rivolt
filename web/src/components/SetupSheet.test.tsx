import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SetupSheet } from "./SetupSheet";
import { stubFetchRoutes } from "../test/fetchRoutes";

// Synthetic: the public sample point, never a real home.
const HOME_UNSET = { set: false, latitude: 0, longitude: 0 };
const HOME_SET = { set: true, latitude: 34.161, longitude: -118.168, label: "Home", address: "Rose Bowl Drive, Pasadena, California", source: "truck" };
const PLACE = { name: "Rose Bowl Drive, Pasadena", country_code: "US", admin1: "California" };
const STATE = { state: "CA", name: "California", price_per_kwh: 0.3258, currency: "USD", source: { name: "EIA", url: "https://example.test/eia", period: "January to August 2026", released: "2026-10-23", checked: "2026-11-13" } };

function renderSheet() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <SetupSheet vehicleID="v1" onClose={() => {}} />
    </QueryClientProvider>,
  );
}

const calls = (fn: ReturnType<typeof stubFetchRoutes>) => fn.mock.calls.map(([u, init]) => `${(init as RequestInit | undefined)?.method ?? "GET"} ${String(u)}`);

describe("SetupSheet", () => {
  beforeEach(() => window.localStorage.clear());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("offers the truck's position as home, shows the place first, and saves it with source truck only on the tap", async () => {
    const f = stubFetchRoutes({
      "/api/rivian-receipts": { body: {} },
      "/api/settings/rivian-receipts": { body: { opt_in: false, available: true } },
      "/api/settings/home-location/place": { body: PLACE },
      "/api/settings/home-location": { body: HOME_UNSET },
      "/api/state/v1": { body: { latitude: 34.161, longitude: -118.168 } },
    });
    renderSheet();
    await waitFor(() => expect(screen.getByText("Rose Bowl Drive, Pasadena, California")).toBeInTheDocument());
    expect(calls(f).some((c) => c.startsWith("PUT /api/settings/home-location"))).toBe(false);
    f.mockClear();
    fireEvent.click(screen.getByRole("button", { name: "Set home here" }));
    await waitFor(() => expect(calls(f).some((c) => c.startsWith("PUT /api/settings/home-location"))).toBe(true));
    const put = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === "PUT")!;
    const body = JSON.parse(String((put[1] as RequestInit).body));
    expect(body).toMatchObject({ set: true, latitude: 34.161, longitude: -118.168, source: "truck", label: "Home" });
  });

  it("with no position says so and saves nothing", async () => {
    const f = stubFetchRoutes({
      "/api/settings/rivian-receipts": { body: { opt_in: false, available: true } },
      "/api/settings/home-location": { body: HOME_UNSET },
      "/api/state/v1": { body: { latitude: 0, longitude: 0 } },
    });
    renderSheet();
    await waitFor(() => expect(screen.getByText(/hasn't reported a position/)).toBeInTheDocument());
    expect(calls(f).some((c) => c.startsWith("PUT"))).toBe(false);
  });

  async function toPriceStep(charging: unknown, place: unknown, statePrice: { status?: number; body: unknown }) {
    const f = stubFetchRoutes({
      "/api/settings/rivian-receipts": { body: { opt_in: false, available: true } },
      "/api/settings/home-location/place": { body: place },
      "/api/settings/home-location": { body: HOME_SET },
      "/api/settings/charging/state-price": statePrice,
      "/api/settings/charging": { body: charging },
    });
    renderSheet();
    await waitFor(() => expect(screen.getByText(/Home is set/)).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    return f;
  }

  it("offers the state average marked estimated with its period, and saves it as state_average", async () => {
    const f = await toPriceStep({ home_price_per_kwh: 0, home_currency: "USD", home_price_source: "" }, PLACE, { body: STATE });
    await waitFor(() => expect(screen.getByText("estimated")).toBeInTheDocument());
    expect(screen.getByText(/January to August 2026 average, residential \(EIA, released 2026-10-23\)/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Source" })).toHaveAttribute("href", "https://example.test/eia");
    expect(calls(f).some((c) => c.includes("state-price?country=US&state=California"))).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: /^Use \$0\.326/ }));
    await waitFor(() => expect(calls(f).some((c) => c.startsWith("PATCH /api/settings/charging"))).toBe(true));
    const patch = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === "PATCH")!;
    expect(JSON.parse(String((patch[1] as RequestInit).body))).toEqual({ home_price_per_kwh: 0.3258, home_price_source: "state_average" });
  });

  it("'Enter mine' saves a typed rate with no source", async () => {
    const f = await toPriceStep({ home_price_per_kwh: 0, home_currency: "USD", home_price_source: "" }, PLACE, { body: STATE });
    await waitFor(() => expect(screen.getByRole("button", { name: "Enter mine" })).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Enter mine" }));
    fireEvent.change(screen.getByLabelText(/Price per kWh/), { target: { value: "0.14" } });
    fireEvent.click(screen.getByRole("button", { name: "Save rate" }));
    await waitFor(() => expect(calls(f).some((c) => c.startsWith("PATCH /api/settings/charging"))).toBe(true));
    const patch = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === "PATCH")!;
    expect(JSON.parse(String((patch[1] as RequestInit).body))).toEqual({ home_price_per_kwh: 0.14 });
  });

  it("offers no default outside the US or without a table row, only a typed rate", async () => {
    await toPriceStep({ home_price_per_kwh: 0, home_currency: "EUR", home_price_source: "" }, { name: "Strasse", country_code: "DE", admin1: "Bayern" }, { status: 404, body: "no default" });
    await waitFor(() => expect(screen.getByLabelText(/Price per kWh \(EUR\)/)).toBeInTheDocument());
    expect(screen.queryByText("estimated")).toBeNull();
    expect(screen.getByText(/no state average to offer/)).toBeInTheDocument();
  });

  it("shows a rate already set, and says when it is the state-average estimate", async () => {
    await toPriceStep({ home_price_per_kwh: 0.3258, home_currency: "USD", home_price_source: "state_average" }, PLACE, { body: STATE });
    await waitFor(() => expect(screen.getByText(/the state average, an estimate/)).toBeInTheDocument());
  });

  it("drops the receipts step where the operator has it off", async () => {
    stubFetchRoutes({
      "/api/settings/rivian-receipts": { body: { opt_in: false, available: false } },
      "/api/settings/home-location": { body: HOME_SET },
    });
    renderSheet();
    await waitFor(() => expect(screen.getByText(/Step 1 of 3/)).toBeInTheDocument());
  });
});
