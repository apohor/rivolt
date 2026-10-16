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

  it("shows a skeleton, not 'No sleep data yet', while sleep loads, and no drain card until it has data", async () => {
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
    expect(screen.queryByLabelText("Parked drain")).toBeNull();
    // No bordered, empty placeholder card above Pack capacity.
    expect(document.querySelector(".animate-pulse.h-32")).toBeNull();
  });
});
