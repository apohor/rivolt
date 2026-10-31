import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { ProSheet } from "./ProSheet";
import { PRO, checkoutLabel, consentText, firstCharge } from "../lib/proCopy";
import { planState } from "../lib/plan";
import foundingMe from "../../../ios/RivoltTests/Fixtures/me_plan_founding.json";
import freeMe from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import stripeMe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import appleMe from "../../../ios/RivoltTests/Fixtures/me_plan_apple.json";
import lapsedMe from "../../../ios/RivoltTests/Fixtures/me_plan_lapsed.json";

const free = planState(freeMe);

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

  it("the first charge is today, or when a founding year ends, and the consent line agrees", () => {
    const founding = planState(foundingMe);
    const when = firstCharge(founding);
    expect(when).toMatch(/^on .*2027/);
    expect(consentText("year", founding)).toContain(`the first charge is ${when}`);
    expect(consentText("year", free)).toContain("the first charge is today");
    expect(consentText("month", planState(lapsedMe))).toContain("the first charge is today");
  });

  it("promises no trial anywhere, and points a free account to the demo with plain links", () => {
    const { container } = renderSheet(free);
    expect(container.ownerDocument.body.textContent).not.toMatch(/trial|14 days of Pro|Keep Pro/i);
    const links = Array.from(container.ownerDocument.querySelectorAll("[data-demo-links] a")) as HTMLAnchorElement[];
    expect(links.map((a) => a.href)).toEqual([
      "https://demo.rivolt.dev/demo/trips/plan",
      "https://demo.rivolt.dev/demo/activity",
      "https://demo.rivolt.dev/demo/truck",
    ]);
    for (const a of links) {
      expect(a.getAttribute("href")).not.toContain("?");
      expect(a.rel).toContain("noopener");
    }
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
