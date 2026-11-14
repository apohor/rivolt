import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AdminSimCard } from "./AdminSimCard";
import { stubFetchRoutes } from "../test/fetchRoutes";

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <AdminSimCard />
    </QueryClientProvider>,
  );
}

describe("the preview's simulated drive and charge card", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("is not drawn where the server has no such routes", async () => {
    const f = stubFetchRoutes({});
    const { container } = wrap();
    await waitFor(() => expect(f).toHaveBeenCalled());
    expect(container.querySelector("[data-admin-sim]")).toBeNull();
  });

  it("starts a charge and shows the run, with both buttons off while it lasts", async () => {
    const run = { id: "r", kind: "charge", start: "2026-11-14T18:00:00Z", end: "2026-11-14T18:28:00Z", added_mi: 0 };
    const f = stubFetchRoutes({ "/api/admin/sim/charge": { body: { active: run } }, "/api/admin/sim": { body: { active: null } } });
    wrap();
    fireEvent.click(await screen.findByRole("button", { name: "Start a simulated charge now" }));
    expect(await screen.findByText(/A simulated charge runs/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start a simulated drive now" })).toBeDisabled();
    expect(f.mock.calls.some((c) => String(c[0]).endsWith("/api/admin/sim/charge") && (c[1] as RequestInit).method === "POST")).toBe(true);
  });

  it("shows why the truck cannot take a run", async () => {
    stubFetchRoutes({
      "/api/admin/sim/drive": { status: 409, body: { error: "the sample truck is away from home; try again when it is back" } },
      "/api/admin/sim": { body: { active: null } },
    });
    wrap();
    fireEvent.click(await screen.findByRole("button", { name: "Start a simulated drive now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("away from home");
  });
});
