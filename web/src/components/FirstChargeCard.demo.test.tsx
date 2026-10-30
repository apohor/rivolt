import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import FirstChargeCard from "./FirstChargeCard";
import type { Charge, Drive } from "../lib/api";

// The demo shows the sample truck's first priced charge as a worked example, whatever came before it: the
// sample truck drove for weeks (July 3) before its first priced charge (July 26), and that must not age the card out.
describe("first-charge card in the demo", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows the first priced charge even when drives came weeks before it", () => {
    Object.defineProperty(window, "location", { value: { pathname: "/demo", search: "" }, writable: true });
    const charges = [
      { ID: "c1", StartedAt: "2026-07-26T14:00:00Z", EnergyAddedKWh: 74.94, Cost: 35.97, MaxPowerKW: 185, MilesAdded: 0, Currency: "USD" },
    ] as unknown as Charge[];
    const drives = [{ ID: "d1", StartedAt: "2026-07-03T08:46:00Z", DistanceMi: 12, EnergyUsedKWh: 4 }] as unknown as Drive[];
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <FirstChargeCard vehicleID="demo-1" charges={charges} drives={drives} demo />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(container.querySelector("[data-first-charge]")?.getAttribute("data-first-charge")).toBe("A");
  });
});
