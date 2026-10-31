import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { DeleteHistoryCard } from "./DeleteHistoryCard";
import { ProLimitNotice } from "./ProLimitNotice";
import { stubFetchRoutes } from "../test/fetchRoutes";

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("the privacy controls on a record the free window hides", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("deletes history before the chosen month only after the box is ticked, and says what went", async () => {
    const f = stubFetchRoutes({ "/api/data/delete-range": { body: { drives: 12, charges: 3, samples: 5000 } } });
    wrap(<DeleteHistoryCard now={new Date(Date.UTC(2026, 9, 31, 12))} />);
    const go = screen.getByRole("button", { name: /Delete history before July 2026/ });
    expect(go).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox"));
    expect(go).toBeEnabled();
    fireEvent.click(go);
    expect(await screen.findByText("Deleted 12 drives, 3 charges and 5,000 telemetry samples.")).toBeInTheDocument();
    const call = f.mock.calls.find((c) => String(c[0]).includes("/api/data/delete-range"))!;
    expect(JSON.parse(String((call[1] as RequestInit).body))).toEqual({ before: "2026-07", confirm: true });
  });

  it("changing the month clears the tick, so the confirmation always names the month it deletes", () => {
    stubFetchRoutes({});
    wrap(<DeleteHistoryCard now={new Date(Date.UTC(2026, 9, 31, 12))} />);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.change(screen.getByLabelText("Delete everything before"), { target: { value: "2026-05" } });
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    expect(screen.getByRole("button", { name: /Delete history before May 2026/ })).toBeDisabled();
  });

  it("an old record's note keeps Export and Delete history in reach", async () => {
    stubFetchRoutes({ "/api/auth/me": { body: { authenticated: true } } });
    wrap(<ProLimitNotice limit={{ code: "outside_free_window", freeDays: 90 }} privacy />);
    await waitFor(() => expect(screen.getByRole("link", { name: "Export everything" })).toHaveAttribute("href", "/settings#export"));
    expect(screen.getByRole("link", { name: "Delete history before a month" })).toHaveAttribute("href", "/settings#delete-history");
  });
});
