import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { PlanCard } from "./PlanCard";
import { PlanNotice } from "./PlanNotice";
import { ProLimitNotice } from "./ProLimitNotice";
import { stubFetchRoutes } from "../test/fetchRoutes";
import billingOff from "../../../ios/RivoltTests/Fixtures/me.json";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import trial from "../../../ios/RivoltTests/Fixtures/me_plan_trial.json";
import stripe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import founding from "../../../ios/RivoltTests/Fixtures/me_plan_founding.json";
import lapsed from "../../../ios/RivoltTests/Fixtures/me_plan_lapsed.json";

const now = new Date("2026-11-11T12:00:00Z");

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
    const { container } = renderWith(billingOff, <><PlanCard now={now} /><PlanNotice /></>);
    await new Promise((r) => setTimeout(r, 30));
    expect(container.querySelector("[data-plan-card],[data-plan-notice]")).toBeNull();
  });

  it("shows the free state and opens the sheet", async () => {
    renderWith(free, <PlanCard now={now} />);
    expect(await screen.findByText("Recording, alerts, the last 90 days and the full export, always.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "See Pro" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("shows the trial with its end, days left and that no card is needed", async () => {
    renderWith(trial, <PlanCard now={now} />);
    expect(await screen.findByText(/Pro trial, ends .*2026/)).toBeInTheDocument();
    expect(screen.getByText(/3 days left\. No card, nothing is charged/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Keep Pro" })).toBeInTheDocument();
  });

  it("calls a founding year free until its date, and a Stripe plan manageable", async () => {
    const a = renderWith(founding, <PlanCard now={now} />);
    expect(await screen.findByText(/Rivolt Pro, free until .*2027/)).toBeInTheDocument();
    expect(screen.getByText(/no card, nothing is charged/)).toBeInTheDocument();
    a.unmount();
    renderWith(stripe, <PlanCard now={now} />);
    expect(await screen.findByText(/Rivolt Pro, renews .*2026/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Manage subscription" })).toBeInTheDocument();
  });

  it("explains a lapse: nothing deleted, the export stays, and the banner can be dismissed once", async () => {
    renderWith(lapsed, <><PlanCard now={now} /><PlanNotice /></>);
    expect((await screen.findAllByText(/Nothing is deleted; older history stays stored and is in your export any time \(Settings → Data\)/)).length).toBe(2);
    expect(screen.getByRole("link", { name: "Export everything" })).toHaveAttribute("href", "/settings#export");
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(document.querySelector("[data-plan-notice]")).toBeNull();
  });

  it("a 402 limit says why, that nothing is lost, and offers Pro only where the server bills", async () => {
    const a = renderWith(free, <ProLimitNotice limit={{ code: "outside_free_window", freeDays: 90 }} />);
    expect(await screen.findByText(/older than the last 90 days/)).toBeInTheDocument();
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
    renderWith(cancelled, <PlanCard now={now} />);
    expect(await screen.findByText(/Rivolt Pro until .*2026/)).toBeInTheDocument();
    expect(screen.getByText("Cancelled; Pro stays on until then. Nothing is deleted after.")).toBeInTheDocument();
    expect(screen.queryByText(/renews/)).toBeNull();
    expect(screen.getByRole("button", { name: "Resubscribe" })).toBeInTheDocument();
  });

  it("a failed payment inside the grace says to update the card, with Pro still on and nothing deleted", async () => {
    renderWith(grace, <><PlanCard now={now} /><PlanNotice /></>);
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
    renderWith(free, <PlanCard now={now} />);
    expect(await screen.findByText("No charge was made.")).toBeTruthy();
    expect(window.location.search).toBe("");
    expect(window.location.hash).toBe("#plan");
  });

  it("thanks the buyer once the plan reads Pro", async () => {
    window.history.replaceState(null, "", "/settings?checkout=success#plan");
    renderWith(stripe, <PlanCard now={now} />);
    expect(await screen.findByText("Thanks, Pro is on.")).toBeTruthy();
    expect(window.location.search).toBe("");
  });

  it("says Pro turns on in a moment while the plan is still free", async () => {
    window.history.replaceState(null, "", "/settings?checkout=success#plan");
    renderWith(free, <PlanCard now={now} />);
    expect(await screen.findByText("Thanks. Pro turns on in a moment.")).toBeTruthy();
  });

  it("shows nothing extra with no ?checkout=", async () => {
    window.history.replaceState(null, "", "/settings#plan");
    const { container } = renderWith(free, <PlanCard now={now} />);
    await waitFor(() => expect(container.querySelector("[data-plan-card]")).not.toBeNull());
    expect(container.querySelector("[data-checkout-return]")).toBeNull();
  });
});
