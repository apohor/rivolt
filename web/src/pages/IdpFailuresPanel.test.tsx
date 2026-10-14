import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { IdpFailuresPanel } from "./AdminPage";
import { stubFetchRoutes } from "../test/fetchRoutes";

const row = (o: Record<string, unknown>) => ({
  id: "f1",
  username: "gone@example.com",
  error: "kratos: 503",
  attempts: 2,
  failed_at: "2026-10-10T10:00:00Z",
  last_tried: "2026-10-11T10:00:00Z",
  account_exists: false,
  ...o,
});

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <IdpFailuresPanel />
    </QueryClientProvider>,
  );
}

describe("IdpFailuresPanel", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("offers Retry only when the account doesn't exist again", async () => {
    stubFetchRoutes({
      "/api/admin/idp-delete-failures": {
        body: [row({}), row({ id: "f2", username: "back@example.com", account_exists: true })],
      },
    });
    renderPanel();
    expect(await screen.findByText("gone@example.com")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Retry" })).toHaveLength(1);
    expect(screen.getByText(/Delete that account instead/)).toBeInTheDocument();
  });

  it("shows the quiet state when there are none", async () => {
    stubFetchRoutes({ "/api/admin/idp-delete-failures": { body: [] } });
    renderPanel();
    expect(await screen.findByText(/None\./)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("posts the retry", async () => {
    const f = stubFetchRoutes({
      "/api/admin/idp-delete-failures": { body: [row({})] },
      "/api/admin/idp-delete-failures/f1/retry": { body: { resolved: true } },
    });
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
    await vi.waitFor(() =>
      expect(JSON.stringify((f as { mock?: { calls: unknown[] } }).mock?.calls ?? [])).toContain("/f1/retry"),
    );
  });
});
