import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { stubFetchRoutes } from "../test/fetchRoutes";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import lapsed from "../../../ios/RivoltTests/Fixtures/me_plan_lapsed.json";

// "Not now" on a Pro prompt (D6): kept per account on the server; the planner note's action goes and its reason line stays; the lapse banner
// moves to the same store and an old browser dismissal counts once.
function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}
const NOTE = "Multi-day plans, overnights and re-plan from here are part of Rivolt Pro. A single-day plan stays free.";
const posts = (f: ReturnType<typeof stubFetchRoutes>) => f.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === "POST").map((c) => String(c[0]));

async function notice(keyOn: boolean) {
  vi.resetModules();
  vi.doMock("../lib/config", async (orig) => ({ ...(await orig<typeof import("../lib/config")>()), useDesignPreview: (k: string) => keyOn && k === "pro_entry" }));
  const { ProLimitNotice } = await import("./ProLimitNotice");
  wrap(<ProLimitNotice limit={{ code: "pro_required" }} feature="planner" />);
}
async function banner(keyOn: boolean) {
  vi.resetModules();
  vi.doMock("../lib/config", async (orig) => ({ ...(await orig<typeof import("../lib/config")>()), useDesignPreview: (k: string) => keyOn && k === "pro_entry" }));
  const { PlanNotice } = await import("./PlanNotice");
  wrap(<PlanNotice />);
}

describe("Not now (pro_entry)", () => {
  beforeEach(() => window.localStorage.clear());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.doUnmock("../lib/config");
    vi.resetModules();
  });

  it("the planner note: Not now removes the action, keeps the reason line, and tells the server", async () => {
    const f = stubFetchRoutes({ "/api/auth/me": { body: free }, "/api/pro/prompts/planner/dismiss": { status: 204, body: "" } });
    await notice(true);
    fireEvent.click(await screen.findByRole("button", { name: "Not now" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "See the Pro tier" })).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Not now" })).not.toBeInTheDocument();
    expect(screen.getByText(NOTE)).toBeInTheDocument();
    expect(posts(f)).toEqual(["/api/pro/prompts/planner/dismiss"]);
  });

  it("the planner note: already hidden on the account, no action from the start", async () => {
    stubFetchRoutes({ "/api/auth/me": { body: { ...free, pro_prompts_hidden: ["planner"] } } });
    await notice(true);
    expect(await screen.findByText(NOTE)).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("button", { name: "See the Pro tier" })).not.toBeInTheDocument());
  });

  it("key off: no Not now, no request to the store", async () => {
    const f = stubFetchRoutes({ "/api/auth/me": { body: free } });
    await notice(false);
    expect(await screen.findByRole("button", { name: "See Pro" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Not now" })).not.toBeInTheDocument();
    expect(posts(f)).toEqual([]);
  });

  it("the lapse banner: Dismiss goes to the store, not the browser", async () => {
    const f = stubFetchRoutes({ "/api/auth/me": { body: lapsed }, "/api/pro/prompts/lapse_banner/dismiss": { status: 204, body: "" } });
    await banner(true);
    fireEvent.click(await screen.findByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(document.querySelector("[data-plan-notice]")).toBeNull());
    expect(posts(f)).toEqual(["/api/pro/prompts/lapse_banner/dismiss"]);
    expect(window.localStorage.getItem("rivolt.planNotice.dismissed")).toBeNull();
  });

  it("the lapse banner: a dismissal saved in the browser is honoured once and moved to the account", async () => {
    window.localStorage.setItem("rivolt.planNotice.dismissed", "2026-10-01");
    const f = stubFetchRoutes({ "/api/auth/me": { body: lapsed }, "/api/pro/prompts/lapse_banner/dismiss": { status: 204, body: "" } });
    await banner(true);
    await waitFor(() => expect(posts(f)).toEqual(["/api/pro/prompts/lapse_banner/dismiss"]));
    expect(document.querySelector("[data-plan-notice]")).toBeNull();
    expect(window.localStorage.getItem("rivolt.planNotice.dismissed")).toBeNull();
  });

  it("the lapse banner, hidden on the account: a browser dismissal is dropped without a second Not now", async () => {
    window.localStorage.setItem("rivolt.planNotice.dismissed", "2026-10-01");
    const f = stubFetchRoutes({ "/api/auth/me": { body: { ...lapsed, pro_prompts_hidden: ["lapse_banner"] } } });
    await banner(true);
    await waitFor(() => expect(window.localStorage.getItem("rivolt.planNotice.dismissed")).toBeNull());
    expect(posts(f)).toEqual([]);
  });

  it("key off: the lapse banner dismisses in the browser as before", async () => {
    const f = stubFetchRoutes({ "/api/auth/me": { body: lapsed } });
    await banner(false);
    fireEvent.click(await screen.findByRole("button", { name: "Dismiss" }));
    expect(window.localStorage.getItem("rivolt.planNotice.dismissed")).toBe("2026-10-01");
    expect(posts(f)).toEqual([]);
  });
});
