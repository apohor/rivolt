import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { ProSheet, consentText, firstChargeLine } from "./ProSheet";
import { PRO } from "../lib/proCopy";

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

  it("shows the price, period and what is included before the button, with Terms and Privacy links", () => {
    renderSheet({ kind: "free" });
    expect(screen.getByText("$3.99")).toBeInTheDocument();
    expect(screen.getByText("$34.99")).toBeInTheDocument();
    for (const x of PRO.included) expect(screen.getByText(x)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Terms" })).toHaveAttribute("href", "/terms");
    expect(screen.getByRole("link", { name: "Privacy" })).toHaveAttribute("href", "/privacy");
    expect(screen.getByText(/Unofficial, not affiliated with Rivian/)).toBeInTheDocument();
  });

  it("keeps Subscribe off until the unticked renewal consent is ticked, and the consent names the price and period", () => {
    renderSheet({ kind: "free" });
    const box = screen.getByRole("checkbox");
    expect(box).not.toBeChecked();
    const subscribe = screen.getByRole("button", { name: "Subscribe" });
    expect(subscribe).toBeDisabled();
    expect(screen.getByText(consentText("year"))).toBeInTheDocument();
    expect(consentText("year")).toMatch(/renews automatically every year at \$34\.99 until I cancel/);
    fireEvent.click(screen.getByLabelText(/Monthly/));
    expect(screen.getByText(consentText("month"))).toBeInTheDocument();
    fireEvent.click(box);
    expect(subscribe).toBeEnabled();
  });

  it("says the web trial needs no card, and when the first payment happens", () => {
    expect(firstChargeLine({ kind: "trial", ends: "2026-12-20T00:00:00Z" })).toMatch(/needs no card and never charges you/);
    expect(firstChargeLine({ kind: "free" })).toMatch(/first payment is today/);
  });

  it("says plainly that billing isn't available while it is mocked, and charges nothing", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 404, headers: { get: () => "text/plain" }, text: async () => "not found" }) as unknown as Response));
    renderSheet({ kind: "free" });
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Subscribe" }));
    expect(await screen.findByText(/Billing isn't available yet\. Nothing was charged\./)).toBeInTheDocument();
  });

  it("offers Manage subscription to a Stripe subscriber and the App Store route to an Apple one, with no Subscribe button", () => {
    const { unmount } = renderSheet({ kind: "pro", source: "stripe", periodEnd: "2027-01-05T00:00:00Z" });
    expect(screen.getByRole("button", { name: PRO.manageStripe })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Subscribe" })).toBeNull();
    unmount();
    renderSheet({ kind: "pro", source: "apple", periodEnd: null });
    expect(screen.getByText(PRO.manageApple)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: PRO.manageStripe })).toBeNull();
  });

  it("opens Checkout at the URL the server answers", async () => {
    const assign = vi.fn();
    Object.defineProperty(window, "location", { value: { pathname: "/settings", search: "", assign }, writable: true });
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify({ url: "https://checkout.example/x" }) }) as unknown as Response));
    renderSheet({ kind: "free" });
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Subscribe" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("https://checkout.example/x"));
  });
});
