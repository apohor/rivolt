import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { backend, type Vehicle } from "../lib/api";
import { noteVehicleScope, resetVehicleScope, scopeSentence, scopeVehicleName } from "../lib/vehicleScope";
import { VehicleScopeNote } from "./VehicleScopeNote";

const v = (id: string, model: string, name = ""): Vehicle => ({ id, model, name, vin: id }) as Vehicle;
const res = (id?: string) => ({ headers: new Headers(id ? { "X-Rivolt-Vehicle-Scope": id } : {}) });

afterEach(() => {
  resetVehicleScope();
  vi.restoreAllMocks();
});

describe("scopeVehicleName", () => {
  it("names the model, and the nickname only when two share a model", () => {
    expect(scopeVehicleName([v("a", "R1T", "Rex"), v("b", "R1S", "Sue")], "a")).toBe("R1T");
    expect(scopeVehicleName([v("a", "R1S", "Rex"), v("b", "R1S", "Sue")], "b")).toBe("Sue");
    expect(scopeVehicleName([v("a", "R1T")], "zzz")).toBeNull();
  });
  it("writes the sentence", () => expect(scopeSentence("R1T")).toBe("Showing R1T. Pro includes all vehicles."));
});

describe("VehicleScopeNote", () => {
  function mount() {
    vi.spyOn(backend, "vehicles").mockResolvedValue([v("a", "R1T", "Rex"), v("b", "R1S", "Sue")]);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <QueryClientProvider client={qc}>
        <VehicleScopeNote />
      </QueryClientProvider>,
    );
  }

  it("shows the sentence from the header on a scoped route, and drops it when a later answer has none", async () => {
    mount();
    expect(screen.queryByRole("note")).toBeNull();
    act(() => noteVehicleScope("/api/ledger/monthly?month=2026-10", res("a")));
    expect(await screen.findByText("Showing R1T. Pro includes all vehicles.")).toBeTruthy();
    act(() => noteVehicleScope("/api/drives?limit=5", res()));
    await waitFor(() => expect(screen.queryByRole("note")).toBeNull());
  });

  it("ignores a header on a route outside the limit", () => {
    mount();
    act(() => noteVehicleScope("/api/vehicles", res("a")));
    expect(screen.queryByRole("note")).toBeNull();
  });
});
