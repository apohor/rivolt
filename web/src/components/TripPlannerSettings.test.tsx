import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { ChargersOnMapCard, MapDefaultsCard } from "./TripPlannerSettings";

const server = {
  drive_mode: "DISTANCE",
  has_adapter: true,
  corridor: "normal",
  start_exclude_mi: 25,
  min_fast_kw: 0,
  layers_on: ["dcfc"],
  preferred_only: false,
  weather_on: true,
  radar_on: false,
};

function stub() {
  const puts: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (!url.startsWith("/api/settings/planner")) return { ok: false, status: 404, headers: { get: () => "text/plain" }, text: async () => "no" } as unknown as Response;
    let body: unknown = server;
    if (init?.method === "PUT") {
      const sent = JSON.parse(String(init.body));
      puts.push(sent);
      body = { ...server, ...sent };
    }
    return { ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify(body), json: async () => body } as unknown as Response;
  }));
  return puts;
}

function renderCards() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ChargersOnMapCard />
        <MapDefaultsCard />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Trip planner settings cards", () => {
  it("shows the server's choices and saves a change with every other setting beside it", async () => {
    const puts = stub();
    renderCards();
    const far = (await screen.findByLabelText("How far from the route")) as HTMLSelectElement;
    await waitFor(() => expect(far.disabled).toBe(false));
    expect(far.value).toBe("normal");
    expect((screen.getByLabelText("Hide chargers near the start") as HTMLSelectElement).value).toBe("25");
    fireEvent.change(far, { target: { value: "wide" } });
    await waitFor(() => expect(puts).toHaveLength(1));
    // The drive mode and adapter are other cards' settings: the save must carry them, not reset them.
    expect(puts[0]).toMatchObject({ corridor: "wide", drive_mode: "DISTANCE", has_adapter: true, start_exclude_mi: 25, layers_on: ["dcfc"], weather_on: true });
    expect(await screen.findByText("Saved.", { selector: "span" })).toBeTruthy();
  });

  it("says what the weather on each leg really shows: temperature, wind, rain and snow (the iPhone says the same)", async () => {
    stub();
    renderCards();
    expect(await screen.findByText("The temperature, wind, rain and snow on each leg, on by default.")).toBeTruthy();
    expect(screen.queryByText(/temperature and wind/)).toBeNull();
  });

  it("turns a layer on in the fixed order, and a toggle saves at once", async () => {
    const puts = stub();
    renderCards();
    const hotels = (await screen.findByLabelText("Hotels")) as HTMLInputElement;
    await waitFor(() => expect(hotels.disabled).toBe(false));
    fireEvent.click(hotels);
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].layers_on).toEqual(["dcfc", "hotels"]);
    const weather = document.getElementById("planner-weather-on") as HTMLElement;
    fireEvent.click(weather);
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1].weather_on).toBe(false);
  });

  it("does not offer a radar default where the server has no radar", async () => {
    stub();
    renderCards();
    await screen.findByLabelText("How far from the route");
    expect(document.getElementById("planner-radar-on")).toBeNull();
  });
});
