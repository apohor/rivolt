import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { DeleteAccountBlock } from "./SettingsPage";
import { DELETE_APPLE_NOTE } from "../lib/proCopy";
import { stubFetchRoutes } from "../test/fetchRoutes";
import apple from "../../../ios/RivoltTests/Fixtures/me_plan_apple.json";
import stripe from "../../../ios/RivoltTests/Fixtures/me_plan_subscriber.json";
import free from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";
import billingOff from "../../../ios/RivoltTests/Fixtures/me.json";

function mount(me: Record<string, unknown>) {
  stubFetchRoutes({ "/api/auth/me": { body: me } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <DeleteAccountBlock />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("the delete-account flow and an App Store subscription", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("tells an Apple subscriber to cancel in iPhone Settings first", async () => {
    mount(apple);
    expect(await screen.findByText(DELETE_APPLE_NOTE)).toBeTruthy();
  });

  it.each([
    ["a Stripe subscriber", stripe],
    ["a free account", free],
    ["billing off", billingOff],
  ])("says nothing about Apple for %s", async (_n, me) => {
    const { container } = mount(me);
    await waitFor(() => expect(screen.getByText("Delete my account…")).toBeTruthy());
    await new Promise((r) => setTimeout(r, 30));
    expect(container.querySelector("[data-delete-apple-note]")).toBeNull();
  });
});
