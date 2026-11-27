import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { PlanCard } from "./PlanCard";
import { REFUND } from "../lib/proCopy";
import { stubFetchRoutes } from "../test/fetchRoutes";
import stripe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import apple from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";

const offer = { eligible: true, until: "2026-12-05T10:00:00Z", amount_cents: 3499, currency: "usd" };

function show(routes: Parameters<typeof stubFetchRoutes>[0]) {
  const fn = stubFetchRoutes(routes);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <PlanCard />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { fn, ...view };
}

describe("Refund and cancel on the Plan card (web Pro)", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("is offered with the amount and the last day, asks once more, and says the refund was made", async () => {
    const { fn, container } = show({ "/api/auth/me": { body: stripe }, "/api/billing/refund": { body: offer } });
    const first = await screen.findByRole("button", { name: REFUND.action });
    expect(container.textContent).toContain("$34.99");
    expect(container.textContent).toContain("free tier");
    expect(container.textContent).toContain("Nothing is deleted");
    // Nothing is refunded by the first press.
    fireEvent.click(first);
    expect(fn.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === "POST")).toBe(false);
    // Changing your mind: Keep Pro goes back to the offer.
    fireEvent.click(screen.getByRole("button", { name: REFUND.keep }));
    expect(screen.getByRole("button", { name: REFUND.action })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: REFUND.action }));
    fireEvent.click(screen.getByRole("button", { name: REFUND.yes }));
    await waitFor(() => expect(container.querySelector("[data-refund-done]")?.textContent).toContain("Refunded $34.99"));
    const posts = fn.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "POST");
    expect(posts).toHaveLength(1);
    expect(String(posts[0][0])).toContain("/api/billing/refund");
  });

  it("says so when the refund is no longer available, and keeps Pro as it was", async () => {
    const { container } = show({ "/api/auth/me": { body: stripe }, "/api/billing/refund": { body: offer } });
    fireEvent.click(await screen.findByRole("button", { name: REFUND.action }));
    stubFetchRoutes({ "/api/auth/me": { body: stripe }, "/api/billing/refund": { status: 409, body: { error: "refund_unavailable", reason: "used" } } });
    fireEvent.click(screen.getByRole("button", { name: REFUND.yes }));
    expect(await screen.findByText(REFUND.gone)).toBeInTheDocument();
    expect(container.querySelector("[data-refund-done]")).toBeNull();
  });

  it("says it could not refund when Stripe is down", async () => {
    show({ "/api/auth/me": { body: stripe }, "/api/billing/refund": { body: offer } });
    fireEvent.click(await screen.findByRole("button", { name: REFUND.action }));
    stubFetchRoutes({ "/api/auth/me": { body: stripe }, "/api/billing/refund": { status: 502, body: { error: "billing_unavailable" } } });
    fireEvent.click(screen.getByRole("button", { name: REFUND.yes }));
    expect(await screen.findByText(REFUND.failed)).toBeInTheDocument();
  });

  it("shows nothing when the server has no offer (window closed, used, or the route is absent)", async () => {
    for (const refund of [{ body: { eligible: false, reason: "window_closed" } }, { body: { eligible: false, reason: "used" } }, { status: 404, body: "not found" }]) {
      const { container, unmount } = show({ "/api/auth/me": { body: stripe }, "/api/billing/refund": refund });
      await screen.findByText(/Rivolt Pro/);
      await waitFor(() => expect(container.querySelector("[data-plan-refund]")).toBeNull());
      expect(screen.queryByRole("button", { name: REFUND.action })).toBeNull();
      unmount();
    }
  });

  it("is not asked for, nor shown, on a free account", async () => {
    const { fn, container } = show({ "/api/auth/me": { body: apple }, "/api/billing/refund": { body: offer } });
    await screen.findByText(/Free/);
    expect(container.querySelector("[data-plan-refund]")).toBeNull();
    expect(fn.mock.calls.some(([u]) => String(u).includes("/api/billing/refund"))).toBe(false);
  });
});
