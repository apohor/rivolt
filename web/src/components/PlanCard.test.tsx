import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { MANAGE_FAILED, PlanCard } from "./PlanCard";
import { PlanNotice } from "./PlanNotice";
import { ProLimitNotice } from "./ProLimitNotice";
import { stubFetchRoutes } from "../test/fetchRoutes";
import billingOff from "../../../ios/RivoltTests/Fixtures/me.json";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import trial from "../../../ios/RivoltTests/Fixtures/me_plan_trial.json";
import stripe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import founding from "../../../ios/RivoltTests/Fixtures/me_plan_founding.json";
import manageOnly from "../../../ios/RivoltTests/Fixtures/me_plan_billing_off_manage.json";
import lapsed from "../../../ios/RivoltTests/Fixtures/me_plan_lapsed.json";


function renderWith(me: Record<string, unknown>, ui: React.ReactNode) {
  stubFetchRoutes({ "/api/auth/me": { body: me } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("PlanCard, PlanNotice and ProLimitNotice, against the server's fixtures", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    window.localStorage.clear();
  });

  it("show nothing where billing is off", async () => {
    const { container } = renderWith(billingOff, <><PlanCard /><PlanNotice /></>);
    await new Promise((r) => setTimeout(r, 30));
    expect(container.querySelector("[data-plan-card],[data-plan-notice]")).toBeNull();
  });

  it("shows the free state and opens the sheet", async () => {
    renderWith(free, <PlanCard />);
    // The fixture predates history_months: no length is claimed.
    expect(await screen.findByText("Recording, alerts, only recent months and the full export, always.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "See Pro" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("Unlimited history and trends (free tier: only recent months)")).toBeInTheDocument();
  });

  it("names the window from history_months (6, or 12 in the notice period)", async () => {
    renderWith({ ...free, history_months: 6 }, <PlanCard />);
    expect(await screen.findByText("Recording, alerts, the last 6 months and the full export, always.")).toBeInTheDocument();
    cleanup();
    vi.unstubAllGlobals();
    renderWith({ ...free, history_months: 12 }, <PlanCard />);
    expect(await screen.findByText("Recording, alerts, the last 12 months and the full export, always.")).toBeInTheDocument();
  });

  it("the lapsed banner's See Pro and the limit note's See Pro each open the sheet, now a lazy chunk", async () => {
    renderWith(lapsed, <PlanNotice />);
    expect(screen.queryByRole("button", { name: "Keep Pro" })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "See Pro" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    cleanup();
    renderWith(free, <ProLimitNotice limit={{ code: "pro_required", limit: 12 } as never} />);
    fireEvent.click(await screen.findByRole("button", { name: "See Pro" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("shows an old trial row as plain Pro, never as a trial or with Keep Pro", async () => {
    const { container } = renderWith(trial, <PlanCard />);
    expect(await screen.findByText(/^Rivolt Pro, free until /)).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/trial|14 days|Keep Pro/i);
  });

  it("calls a founding year free until its date, and a Stripe plan manageable", async () => {
    const a = renderWith(founding, <PlanCard />);
    expect(await screen.findByText(/Rivolt Pro, free until .*2027/)).toBeInTheDocument();
    expect(screen.getByText(/no card, nothing is charged/)).toBeInTheDocument();
    a.unmount();
    renderWith(stripe, <PlanCard />);
    expect(await screen.findByText(/Rivolt Pro, renews .*2026/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Manage subscription" })).toBeInTheDocument();
  });

  it("shows an operator grant as free until its date, with no promise of an email and no action", async () => {
    const granted = { ...founding, plan_source: "grant", plan_period_end: "2027-03-04T23:59:59Z", plan_period_end_date: "2027-03-04" };
    const { container } = renderWith(granted, <PlanCard />);
    expect(await screen.findByText("Rivolt Pro, free until March 4, 2027")).toBeInTheDocument();
    expect(screen.getByText(/Yours at no charge until then; no card, nothing is charged\. Nothing is deleted when it ends\./)).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/email you|first year|trial|Keep Pro/i);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("explains a lapse: nothing deleted, the export stays, and the banner can be dismissed once", async () => {
    renderWith(lapsed, <><PlanCard /><PlanNotice /></>);
    expect((await screen.findAllByText(/Nothing is deleted; older history stays stored and is in your export any time \(Settings → Data\)/)).length).toBe(2);
    expect(screen.getByRole("link", { name: "Export everything" })).toHaveAttribute("href", "/settings#export");
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(document.querySelector("[data-plan-notice]")).toBeNull();
  });

  it("the lapsed Plan card says Pro ended and offers See Pro, the free card's label, not Keep Pro", async () => {
    renderWith(lapsed, <PlanCard />);
    expect(await screen.findByText(/Your Pro ended on/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "See Pro" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Keep Pro" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "See Pro" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("a 402 limit says why, that nothing is lost, and offers Pro only where the server bills", async () => {
    const a = renderWith(free, <ProLimitNotice limit={{ code: "outside_free_window", freeFrom: "2025-11-01" }} />);
    expect(await screen.findByText(/before November 1, 2025/)).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "See Pro" })).toBeInTheDocument();
    a.unmount();
    renderWith(billingOff, <ProLimitNotice limit={{ code: "pro_required" }} />);
    expect(await screen.findByText(/part of Rivolt Pro/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "See Pro" })).toBeNull();
  });
});

import cancelled from "../../../ios/RivoltTests/Fixtures/me_plan_cancelled.json";
import grace from "../../../ios/RivoltTests/Fixtures/me_plan_grace.json";

describe("a cancelled subscription and a failed payment", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    window.localStorage.clear();
  });

  it("a cancelled but paid-up plan says so and never says it renews", async () => {
    renderWith(cancelled, <PlanCard />);
    expect(await screen.findByText(/^Rivolt Pro until .*2026$/)).toBeInTheDocument();
    expect(screen.getByText("Cancelled; Pro stays on until then. Nothing is deleted after.")).toBeInTheDocument();
    expect(screen.queryByText(/renews/)).toBeNull();
    expect(screen.getByRole("button", { name: "Resubscribe" })).toBeInTheDocument();
  });

  it("a failed payment inside the grace says to update the card, with Pro still on and nothing deleted", async () => {
    renderWith(grace, <><PlanCard /><PlanNotice /></>);
    expect(await screen.findByText("Rivolt Pro, payment failed")).toBeInTheDocument();
    expect(screen.getByText(/Update your card by .*2026 to keep Pro\. Nothing is deleted either way\./)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Update card" })).toHaveLength(2); // the card and the banner
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent(/A payment for Rivolt Pro failed\. Update your card in Settings → Account → Plan by .*2026/);
    expect(banner).toHaveTextContent("nothing is deleted");
  });

  it("does not show the grace banner for a healthy plan", async () => {
    renderWith(stripe, <PlanNotice />);
    await new Promise((r) => setTimeout(r, 30));
    expect(document.querySelector("[data-plan-notice]")).toBeNull();
  });
});

describe("PlanCard on the way back from Stripe", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
    window.history.replaceState(null, "", "/");
  });

  it("says no charge was made when the buyer backed out, and drops ?checkout=", async () => {
    window.history.replaceState(null, "", "/settings?checkout=cancelled#plan");
    renderWith(free, <PlanCard />);
    expect(await screen.findByText("No charge was made.")).toBeTruthy();
    expect(window.location.search).toBe("");
    expect(window.location.hash).toBe("#plan");
  });

  it("thanks the buyer once the plan reads Pro", async () => {
    window.history.replaceState(null, "", "/settings?checkout=success#plan");
    renderWith(stripe, <PlanCard />);
    expect(await screen.findByText("Thanks, Pro is on.")).toBeTruthy();
    expect(window.location.search).toBe("");
  });

  it("says Pro turns on in a moment while the plan is still free", async () => {
    window.history.replaceState(null, "", "/settings?checkout=success#plan");
    renderWith(free, <PlanCard />);
    expect(await screen.findByText("Thanks. Pro turns on in a moment.")).toBeTruthy();
  });

  it("shows nothing extra with no ?checkout=", async () => {
    window.history.replaceState(null, "", "/settings#plan");
    const { container } = renderWith(free, <PlanCard />);
    await waitFor(() => expect(container.querySelector("[data-plan-card]")).not.toBeNull());
    expect(container.querySelector("[data-checkout-return]")).toBeNull();
  });
});

describe("PlanCard with billing off and a live Stripe subscription (a rollback)", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows Manage subscription and nothing else about plans, and opens the portal", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign, search: "" });
    stubFetchRoutes({ "/api/auth/me": { body: manageOnly }, "/api/billing/portal": { body: { url: "https://billing.example/portal" } } });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <PlanCard />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const button = await screen.findByRole("button", { name: "Manage subscription" });
    expect(container.querySelector("[data-plan-card='manage-only']")).not.toBeNull();
    expect(container.textContent).not.toMatch(/See Pro|Subscribe|\$\d|trial|Keep Pro/i);
    fireEvent.click(button);
    await waitFor(() => expect(assign).toHaveBeenCalledWith("https://billing.example/portal"));
  });

  it("says how to cancel if the portal can't be opened, never that nothing was charged", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign, search: "" });
    stubFetchRoutes({ "/api/auth/me": { body: manageOnly }, "/api/billing/portal": { status: 502, body: { error: "upstream" } } });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <PlanCard />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Manage subscription" }));
    expect(await screen.findByText(MANAGE_FAILED)).toBeInTheDocument();
    expect(container.textContent).toContain("support@rivolt.dev");
    expect(container.textContent).not.toMatch(/Nothing was charged/);
    expect(assign).not.toHaveBeenCalled();
  });

  it("stays hidden with billing off and no subscription", async () => {
    const { container } = renderWith(billingOff, <PlanCard />);
    await new Promise((r) => setTimeout(r, 30));
    expect(container.querySelector("[data-plan-card]")).toBeNull();
  });
});
