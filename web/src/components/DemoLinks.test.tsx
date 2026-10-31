import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { ProLimitNotice } from "./ProLimitNotice";
import { demoLinksFor } from "../lib/demoLinks";
import { stubFetchRoutes } from "../test/fetchRoutes";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import billingOff from "../../../ios/RivoltTests/Fixtures/me.json";

function wrap(me: Record<string, unknown>, ui: React.ReactNode) {
  stubFetchRoutes({ "/api/auth/me": { body: me } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("see it in the demo", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("maps a limit to the demo pages that show it", () => {
    expect(demoLinksFor({ feature: "planner", code: "pro_required" }).map((l) => l.href)).toEqual(["https://demo.rivolt.dev/demo/trips/plan"]);
    expect(demoLinksFor({ code: "outside_free_window" }).map((l) => l.href)).toEqual(["https://demo.rivolt.dev/demo/activity", "https://demo.rivolt.dev/demo/truck"]);
    expect(demoLinksFor({ feature: "vehicle", code: "pro_required" })).toEqual([]);
  });

  it("the planner's limit note links to the demo planner on a billing server, and nowhere with billing off", async () => {
    const a = wrap(free, <ProLimitNotice limit={{ code: "pro_required" }} feature="planner" />);
    const link = await screen.findByRole("link", { name: "See the multi-day planner in the demo" });
    expect(link.getAttribute("href")).toBe("https://demo.rivolt.dev/demo/trips/plan");
    expect(link.getAttribute("rel")).toContain("noopener");
    a.unmount();
    wrap(billingOff, <ProLimitNotice limit={{ code: "pro_required" }} feature="planner" />);
    await new Promise((r) => setTimeout(r, 30));
    expect(document.querySelector("[data-demo-links]")).toBeNull();
  });
});
