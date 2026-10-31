import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import full from "../../../ios/RivoltTests/Fixtures/year_in_rivolt.json";
import notYet from "../../../ios/RivoltTests/Fixtures/year_in_rivolt_not_yet.json";
import { stubFetchRoutes } from "../test/fetchRoutes";
import { YearCard } from "./YearCard";

function renderCard(now: Date) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <YearCard now={now} />
    </QueryClientProvider>,
  );
}

describe("YearCard", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    Object.defineProperty(window, "location", { value: { pathname: "/money", search: "" }, writable: true });
  });

  it("shows in January for an eligible account", async () => {
    Object.defineProperty(window, "location", { value: { pathname: "/money", search: "" }, writable: true });
    stubFetchRoutes({ "/api/year-in-rivolt": { body: full } });
    renderCard(new Date(2027, 0, 4));
    expect(await screen.findByText("2026 in your R1S")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "See your year" })).toBeInTheDocument();
  });

  it("shows nothing for an account that is not eligible yet", async () => {
    Object.defineProperty(window, "location", { value: { pathname: "/money", search: "" }, writable: true });
    stubFetchRoutes({ "/api/year-in-rivolt": { body: notYet } });
    const { container } = renderCard(new Date(2027, 0, 4));
    await new Promise((r) => setTimeout(r, 30));
    expect(container.querySelector("[data-year-card]")).toBeNull();
  });

  it("asks the server for nothing outside January or in the demo", async () => {
    const f = stubFetchRoutes({ "/api/year-in-rivolt": { body: full } });
    Object.defineProperty(window, "location", { value: { pathname: "/money", search: "" }, writable: true });
    renderCard(new Date(2026, 9, 30));
    Object.defineProperty(window, "location", { value: { pathname: "/demo/money", search: "" }, writable: true });
    renderCard(new Date(2027, 0, 4));
    await new Promise((r) => setTimeout(r, 30));
    expect(document.querySelector("[data-year-card]")).toBeNull();
    expect(JSON.stringify((f as unknown as { mock?: { calls: unknown[] } })?.mock?.calls ?? [])).not.toContain("year-in-rivolt");
  });
});
