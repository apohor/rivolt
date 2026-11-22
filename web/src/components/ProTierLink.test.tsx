import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { ProTierLink } from "./ProTierLink";
import { stubFetchRoutes } from "../test/fetchRoutes";
import billingOff from "../../../ios/RivoltTests/Fixtures/me.json";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import stripe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import founding from "../../../ios/RivoltTests/Fixtures/me_plan_founding.json";
import lapsed from "../../../ios/RivoltTests/Fixtures/me_plan_lapsed.json";

let keyOn = true;
vi.mock("../lib/config", async (orig) => ({ ...(await orig<typeof import("../lib/config")>()), useDesignPreview: (k: string) => k === "pro_entry" && keyOn }));

function renderWith(me: Record<string, unknown>) {
  stubFetchRoutes({ "/api/auth/me": { body: me } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ProTierLink />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  keyOn = true;
});

describe("the header's Pro tier link (design key pro_entry)", () => {
  it("shows for a free account and a lapsed one, and one tap opens the sheet", async () => {
    renderWith(free);
    const link = await screen.findByRole("button", { name: "Pro tier" });
    fireEvent.click(link);
    await screen.findByRole("dialog");
    cleanup();
    renderWith(lapsed);
    await screen.findByRole("button", { name: "Pro tier" });
  });

  it("never shows for Pro, founding, or billing off", async () => {
    for (const me of [stripe, founding, billingOff, { ...free, plan_source: "demo" }]) {
      const { container } = renderWith(me);
      await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled());
      await new Promise((r) => setTimeout(r, 20));
      expect(screen.queryByRole("button", { name: "Pro tier" })).toBeNull();
      expect(container.textContent).toBe("");
      cleanup();
    }
  });

  it("is absent with the key off, and does not even ask for the account", async () => {
    keyOn = false;
    const fetch = stubFetchRoutes({ "/api/auth/me": { body: free } });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={qc}>
        <ProTierLink />
      </QueryClientProvider>,
    );
    await new Promise((r) => setTimeout(r, 20));
    expect(container.textContent).toBe("");
    expect(fetch).not.toHaveBeenCalled();
  });
});
