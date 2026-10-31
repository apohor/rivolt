import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { PlanCard } from "./PlanCard";
import { PlanNotice } from "./PlanNotice";
import { stubFetchRoutes } from "../test/fetchRoutes";

const base = { authenticated: true, user_id: "u1", username: "a", role: "user", onboarding_completed: true };
const now = new Date("2026-12-10T12:00:00Z");

function renderWith(me: Record<string, unknown>, ui: React.ReactNode) {
  stubFetchRoutes({ "/api/auth/me": { body: { ...base, ...me } } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("PlanCard and PlanNotice", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    window.localStorage.clear();
  });

  it("show nothing where the server does not bill", async () => {
    const { container } = renderWith({}, <><PlanCard now={now} /><PlanNotice now={now} /></>);
    await new Promise((r) => setTimeout(r, 30));
    expect(container.querySelector("[data-plan-card],[data-plan-notice]")).toBeNull();
  });

  it("shows the free state and opens the sheet", async () => {
    renderWith({ plan: "free" }, <PlanCard now={now} />);
    expect(await screen.findByText(/Free: the last 90 days/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "See Rivolt Pro" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("shows the trial with its days left and that no card is needed", async () => {
    renderWith({ plan: "pro", source: "trial", trial_ends: "2026-12-13T12:00:00Z" }, <PlanCard now={now} />);
    expect(await screen.findByText(/Pro trial: 3 days left/)).toBeInTheDocument();
    expect(screen.getByText(/needs no card/)).toBeInTheDocument();
  });

  it("calls a founding year 'on us' with no charge, and a Stripe plan manageable", async () => {
    const a = renderWith({ plan: "pro", source: "founding", period_end: "2027-11-01T00:00:00Z" }, <PlanCard now={now} />);
    expect(await screen.findByText(/Pro is on us until/)).toBeInTheDocument();
    a.unmount();
    renderWith({ plan: "pro", source: "stripe", period_end: "2027-01-05T00:00:00Z" }, <PlanCard now={now} />);
    expect(await screen.findByRole("button", { name: "Manage subscription" })).toBeInTheDocument();
  });

  it("explains a lapse: nothing deleted, the export stays, and the notice can be dismissed once", async () => {
    renderWith({ plan: "free", period_end: "2026-12-01T00:00:00Z" }, <><PlanCard now={now} /><PlanNotice now={now} /></>);
    expect(await screen.findByText(/everything older is kept, stays in your export/)).toBeInTheDocument();
    const notice = await screen.findByText(/Nothing is deleted, and you can export everything any time/);
    expect(notice).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Export" })).toHaveAttribute("href", "/settings#export");
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText(/you can export everything any time/)).toBeNull();
  });
});
