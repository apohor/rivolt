import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { FirstWeekLine, WEEK_LINE_WITH_SCHEDULE, WEEK_LINE_WITHOUT_SCHEDULE } from "./FirstWeekLine";
import { stubFetchRoutes } from "../test/fetchRoutes";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const view = () =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <FirstWeekLine />
      </MemoryRouter>
    </QueryClientProvider>,
  );
const answer = (o: object) => ({ "/api/settings/charging/offpeak": { body: { tz: "UTC", windows: [], base_rate: 0.15, currency: "USD", configured: false, flat_rate: false, ...o } } });

describe("the day-7 line on the first-charge card", () => {
  it("points to Money and to the off-peak setting when no schedule is set", async () => {
    stubFetchRoutes(answer({}));
    view();
    expect(await screen.findByText(WEEK_LINE_WITHOUT_SCHEDULE)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open Money" })).toHaveAttribute("href", expect.stringContaining("/money"));
    expect(screen.getByRole("link", { name: "Set off-peak hours" })).toHaveAttribute("href", expect.stringContaining("/settings?tab=charging#offpeak"));
  });
  it("points only to Money once a schedule (or a flat-rate choice) is set", async () => {
    stubFetchRoutes(answer({ configured: true }));
    view();
    expect(await screen.findByText(WEEK_LINE_WITH_SCHEDULE)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open Money" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Set off-peak hours" })).not.toBeInTheDocument();
  });
  it("has no figures, calls the savings an estimate in both, and never claims a price is at the owner's rate (DA)", () => {
    expect(WEEK_LINE_WITH_SCHEDULE + WEEK_LINE_WITHOUT_SCHEDULE).not.toMatch(/\$|\d/);
    expect(WEEK_LINE_WITH_SCHEDULE).toMatch(/an estimate of what moving charges/);
    expect(WEEK_LINE_WITHOUT_SCHEDULE).toMatch(/an estimate of what moving charges/);
    expect(WEEK_LINE_WITH_SCHEDULE + WEEK_LINE_WITHOUT_SCHEDULE).not.toMatch(/priced at/i);
  });
});
