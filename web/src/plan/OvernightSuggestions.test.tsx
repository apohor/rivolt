import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { OvernightCandidatesResponse, TripRoute } from "../lib/api";

const { overnightCandidates } = vi.hoisted(() => ({ overnightCandidates: vi.fn() }));
vi.mock("../lib/api", async (orig) => ({ ...(await orig<typeof import("../lib/api")>()), backend: { overnightCandidates } }));
vi.mock("../lib/demo", () => ({ inDemo: () => false }));
vi.mock("../components/TripRouteMap", () => ({ decodePolyline6: () => [] }));
import { OvernightSuggestions } from "./OvernightSuggestions";

const route = {
  TotalDriveDurationSec: 55_800,
  Waypoints: [
    { Latitude: 45.52, Longitude: -122.68 },
    { Latitude: 38.58, Longitude: -121.49 },
  ],
} as unknown as TripRoute;

const base = { candidates: [], target: { lat: 43, lon: -120, along_m: 288_000, reaches_end: false, window_before_min: 180, window_after_min: 60 }, route_m: 1_530_000, source: "x" };

function show(answer: Partial<OvernightCandidatesResponse>, onUse = vi.fn()) {
  overnightCandidates.mockResolvedValue({ ...base, ...answer });
  render(
    <QueryClientProvider client={new QueryClient()}>
      <OvernightSuggestions route={route} day={1} onUse={onUse} />
    </QueryClientProvider>,
  );
  return onUse;
}

describe("OvernightSuggestions with no stay that lists a charger", () => {
  afterEach(cleanup);

  it("offers hotels with no charger, says so plainly, names the morning charger and credits OpenStreetMap", async () => {
    const onUse = show({
      fallback_stays: [
        {
          name: "Sample Motel", lat: 43.5, lon: -120.5, kind: "hotel", no_onsite_charger: true, detour_m: 1200, along_m: 300_000, source: "osm", attribution: "© OpenStreetMap contributors",
          fast_charger: { name: "Sample Fast Charge A", lat: 43.51, lon: -120.49, entity_id: "e1", distance_m: 3200, max_power_kw: 250 },
        },
      ],
      attribution: "© OpenStreetMap contributors",
      attribution_license_url: "https://opendatacommons.org/licenses/odbl/1-0/",
      attribution_source_url: "https://www.openstreetmap.org/copyright",
    });
    await screen.findByText("Sample Motel");
    expect(screen.getByText(/No charger listed here\. Nearest fast charger: Sample Fast Charge A/)).toBeTruthy();
    expect(screen.getByText(/These hotels list no charger/)).toBeTruthy();
    const credit = document.querySelector("[data-osm-credit]")!;
    expect(credit.textContent).toContain("© OpenStreetMap contributors");
    expect(credit.querySelector('a[href="https://opendatacommons.org/licenses/odbl/1-0/"]')?.textContent).toBe("ODbL licence");
    fireEvent.click(screen.getByRole("button", { name: /Use Sample Motel as the overnight stop, with no charging/ }));
    expect(onUse).toHaveBeenCalledWith({ lat: 43.5, lon: -120.5, label: "Sample Motel", l2Kw: 0, thenVia: { lat: 43.51, lon: -120.49, label: "Sample Fast Charge A" } });
  });

  it("names the nearest listed stay on each side when there is nothing else", async () => {
    show({ nearest: { before: { name: "Sample Inn West", drive_sec: 18_000, offset_sec: -11_880 } as never } });
    await waitFor(() => expect(screen.getByText(/Nearest listed stay: Sample Inn West, about 3\.3 h short of the day/)).toBeTruthy());
    expect(document.querySelector("[data-osm-credit]")).toBeNull();
  });
});
