import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { TripRecapCard } from "./SettingsPage";
import { stubFetchRoutes } from "../test/fetchRoutes";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const base = { timezone: "UTC", timezone_stored: true, weekly_recap: false, plug_threshold_pct: 40, plug_remind_at: "20:00" };
const view = () =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <TripRecapCard />
      </MemoryRouter>
    </QueryClientProvider>,
  );

describe("Settings → Trip recap", () => {
  it("is off until chosen, says what it does, and saves the switch", async () => {
    stubFetchRoutes({ "/api/settings/engagement": { body: { ...base, trip_recap: false } } });
    view();
    const sw = await screen.findByRole("switch", { name: "Trip recap" });
    expect(sw).not.toBeChecked();
    expect(screen.getByText(/A notification after you drive a saved trip, showing how it went against the plan\. Off until you turn it on\./)).toBeInTheDocument();
    const fn = stubFetchRoutes({ "/api/settings/engagement": { body: { ...base, trip_recap: true } } });
    await act(async () => {
      fireEvent.click(sw);
      await new Promise((r) => setTimeout(r, 50));
    });
    const put = fn.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === "PUT");
    expect(JSON.parse((put?.[1] as RequestInit).body as string)).toMatchObject({ trip_recap: true });
    expect(screen.getByRole("switch", { name: "Trip recap" })).toBeChecked();
  });

  it("is hidden on a server that has no such setting", async () => {
    stubFetchRoutes({ "/api/settings/engagement": { body: base } });
    const { container } = view();
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(container).toBeEmptyDOMElement();
  });
});
