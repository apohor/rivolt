import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { ProSheet } from "./ProSheet";
import { PRO, checkoutLabel, consentText, firstCharge, trialLine } from "../lib/proCopy";
import { planState } from "../lib/plan";
import trialMe from "../../../ios/RivoltTests/Fixtures/me_plan_trial.json";
import freeMe from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import stripeMe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import appleMe from "../../../ios/RivoltTests/Fixtures/me_plan_apple.json";
import lapsedMe from "../../../ios/RivoltTests/Fixtures/me_plan_lapsed.json";

const free = planState(freeMe);
const trial = planState(trialMe);

function renderSheet(state: Parameters<typeof ProSheet>[0]["state"]) {
  return render(
    <MemoryRouter>
      <ProSheet state={state} onClose={() => {}} />
    </MemoryRouter>,
  );
}

describe("ProSheet", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows the title, the inclusions and both prices before any button, with nothing pre-selected", () => {
    renderSheet(free);
    expect(screen.getByText(PRO.title)).toBeInTheDocument();
    for (const x of PRO.included) expect(screen.getByText(x)).toBeInTheDocument();
    expect(screen.getByText("$34.99")).toBeInTheDocument();
    expect(screen.getByText("$3.99")).toBeInTheDocument();
    for (const r of screen.getAllByRole("radio")) expect(r).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Choose a plan" })).toBeDisabled();
    expect(screen.getByText(PRO.unofficial)).toBeInTheDocument();
  });

  it("keeps the button off until a plan is chosen and the unticked renewal consent is ticked, and names price, period and the first charge", () => {
    renderSheet(free);
    fireEvent.click(screen.getByLabelText(/Yearly/));
    const box = screen.getByRole("checkbox");
    expect(box).not.toBeChecked();
    const subscribe = screen.getByRole("button", { name: checkoutLabel("year") });
    expect(subscribe).toBeDisabled();
    expect(screen.getByText(new RegExp("renews automatically at \\$34\\.99 a year until I cancel, that the first charge is today"))).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Terms" })).toHaveAttribute("href", "/terms");
    expect(screen.getByRole("link", { name: "Privacy" })).toHaveAttribute("href", "/privacy");
    fireEvent.click(box);
    expect(subscribe).toBeEnabled();
    fireEvent.click(screen.getByLabelText(/Monthly/));
    expect(screen.getByRole("button", { name: checkoutLabel("month") })).toBeDisabled();
    expect(checkoutLabel("month")).toBe("Subscribe: $3.99 a month, renews monthly");
  });

  it("a purchase during the trial starts billing when the trial ends, and the consent line agrees with that date", () => {
    expect(trial.kind).toBe("trial");
    const when = firstCharge(trial);
    expect(when).toMatch(/^on .*2026/);
    expect(consentText("year", trial)).toContain(`the first charge is ${when}`);
    expect(consentText("year", free)).toContain("the first charge is today");
    expect(consentText("month", planState(lapsedMe))).toContain("the first charge is today");
    renderSheet(trial);
    expect(screen.getByText(/You have 14 days of Pro free, with no card/)).toBeInTheDocument();
    expect(trialLine(planState(lapsedMe))).toBeNull();
    expect(trialLine({ kind: "lapsed", endedOn: "2026-11-01T00:00:00Z", endedSource: "trial" })).toMatch(/14 days of Pro were free when you signed up/);
  });

  it("says plainly that billing isn't available while it is mocked, and charges nothing", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 404, headers: { get: () => "text/plain" }, text: async () => "not found" }) as unknown as Response));
    renderSheet(free);
    fireEvent.click(screen.getByLabelText(/Yearly/));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: checkoutLabel("year") }));
    expect(await screen.findByText(PRO.billingUnavailable)).toBeInTheDocument();
  });

  it("offers Manage subscription to a Stripe subscriber and the App Store route to an Apple one, with no Subscribe button", () => {
    const { unmount } = renderSheet(planState(stripeMe));
    expect(screen.getByRole("button", { name: PRO.manageStripe })).toBeInTheDocument();
    expect(screen.getByText(PRO.manageStripeHelp)).toBeInTheDocument();
    expect(screen.queryByRole("radio")).toBeNull();
    unmount();
    renderSheet(planState(appleMe));
    expect(screen.getByText(PRO.manageApple)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: PRO.manageStripe })).toBeNull();
  });

  it("opens Checkout at the URL the server answers", async () => {
    const assign = vi.fn();
    Object.defineProperty(window, "location", { value: { pathname: "/settings", search: "", assign }, writable: true });
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify({ url: "https://checkout.example/x" }) }) as unknown as Response));
    renderSheet(free);
    fireEvent.click(screen.getByLabelText(/Monthly/));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: checkoutLabel("month") }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("https://checkout.example/x"));
  });
});
