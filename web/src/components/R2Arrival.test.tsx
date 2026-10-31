import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { R2ArrivalControl } from "./R2Arrival";

const base = { timezone: "America/Chicago", timezone_stored: true, weekly_recap: false, plug_threshold_pct: 20, plug_remind_at: "20:00" };

// A fetch that serves GET /api/settings/engagement from `state` and applies a PUT to it.
function stub(initial: Record<string, unknown>) {
  const state = { ...initial };
  const puts: Record<string, unknown>[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        puts.push(body);
        Object.assign(state, body);
      }
      return { ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify(state) } as unknown as Response;
    }),
  );
  return puts;
}

function renderIt(where: "guide" | "settings") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <R2ArrivalControl where={where} now={new Date(2026, 9, 31)} />
    </QueryClientProvider>,
  );
}

describe("R2ArrivalControl", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    window.localStorage.clear();
  });

  it("is off by default and saves only the chosen month", async () => {
    const puts = stub({ ...base, r2_arrival_month: null });
    renderIt("guide");
    expect(await screen.findByText("Expecting an R2?")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Expected delivery month"), { target: { value: "2026-12" } });
    fireEvent.click(screen.getByRole("button", { name: "Remind me that month" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].r2_arrival_month).toBe("2026-12");
    expect(await screen.findByText("December 2026")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
  });

  it("deletes the month and says so", async () => {
    const puts = stub({ ...base, r2_arrival_month: "2026-12" });
    renderIt("settings");
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].r2_arrival_month).toBeNull();
    expect(await screen.findByText("Reminder removed.")).toBeInTheDocument();
  });

  it("stays away on the guide after 'No thanks'", async () => {
    stub({ ...base, r2_arrival_month: null });
    const { container } = renderIt("guide");
    fireEvent.click(await screen.findByRole("button", { name: "No thanks" }));
    expect(container.querySelector("[data-r2-arrival]")).toBeNull();
    cleanup();
    const again = renderIt("guide");
    await new Promise((r) => setTimeout(r, 30));
    expect(again.container.querySelector("[data-r2-arrival]")).toBeNull();
  });

  it("shows nothing against a server that has no such setting", async () => {
    stub({ ...base });
    const { container } = renderIt("settings");
    await new Promise((r) => setTimeout(r, 30));
    expect(container.querySelector("[data-r2-arrival]")).toBeNull();
  });
});
