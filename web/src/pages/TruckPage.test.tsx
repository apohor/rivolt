import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import TruckPage from "./TruckPage";

// While data is in flight the Truck tab must not show final-state
// copy ("No sleep data yet") or a bordered placeholder that never fills.
function stubFetch(handlers: Record<string, () => Promise<{ status: number; body: unknown }>>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input).split("?")[0];
      const key = Object.keys(handlers).sort((a, b) => b.length - a.length).find((k) => path.startsWith(k));
      const { status, body } = key ? await handlers[key]() : { status: 404, body: "nf" };
      return {
        ok: status < 300,
        status,
        headers: { get: () => "application/json" },
        text: async () => JSON.stringify(body),
        json: async () => body,
      } as unknown as Response;
    }),
  );
}
const never = () => new Promise<never>(() => {});
const ok = (body: unknown) => async () => ({ status: 200, body });

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <TruckPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("TruckPage loading states", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    localStorage.clear();
  });

  it("shows skeletons, not 'No sleep data yet' or a missing drain card, while they load", async () => {
    stubFetch({
      "/api/settings/rivian": ok({ enabled: true, authenticated: true, mfa_pending: false }),
      "/api/vehicles/v1/drain": never,
      "/api/vehicles/v1/state": ok({}),
      "/api/vehicles/v1/pack-health": ok({ samples: [], headline: {} }),
      "/api/sleep-activity": never,
      "/api/vehicles": ok([{ id: "v1", name: "Tank" }]),
      "/api/state": ok({}),
    });
    renderPage();
    await waitFor(() => expect(screen.getByText("Sleep and idle-awake")).toBeInTheDocument());
    expect(screen.queryByText(/No sleep data yet/)).toBeNull();
    expect(screen.getByLabelText("Loading sleep data")).toBeInTheDocument();
    // The drain slot shows a titled skeleton while it loads (never an empty
    // bordered box), and it is not the real card yet.
    const skel = screen.getByTestId("drain-skeleton");
    expect(skel).toHaveTextContent("Parked drain");
    expect(screen.queryByLabelText("Parked drain")).toBeNull();
  });

  it("removes the drain skeleton when the endpoint is absent (404): no card, no empty box", async () => {
    stubFetch({
      "/api/settings/rivian": ok({ enabled: true, authenticated: true, mfa_pending: false }),
      "/api/vehicles/v1/drain": async () => ({ status: 404, body: "not found" }),
      "/api/vehicles/v1/state": ok({}),
      "/api/vehicles/v1/pack-health": ok({ samples: [], headline: {} }),
      "/api/sleep-activity": ok([]),
      "/api/vehicles": ok([{ id: "v1", name: "Tank" }]),
      "/api/state": ok({}),
    });
    renderPage();
    await waitFor(() => expect(screen.getByText("Sleep and idle-awake")).toBeInTheDocument());
    await waitFor(() => expect(screen.queryByTestId("drain-skeleton")).toBeNull());
    expect(screen.queryByLabelText("Parked drain")).toBeNull();
  });
});
