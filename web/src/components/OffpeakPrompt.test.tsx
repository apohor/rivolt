import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { OffpeakPrompt, OFFPEAK_PROMPT_KEY, offpeakPromptApplies } from "./OffpeakPrompt";
import { stubFetchRoutes } from "../test/fetchRoutes";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

const view = (homeRateSet = true) =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <OffpeakPrompt homeRateSet={homeRateSet} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
const answer = (o: object) => ({ "/api/settings/charging/offpeak": { body: { tz: "UTC", windows: [], base_rate: 0.15, currency: "USD", configured: false, flat_rate: false, ...o } } });

describe("the off-peak prompt on Money", () => {
  it("applies only with a home rate, no hours and no flat-rate choice", () => {
    expect(offpeakPromptApplies({ configured: false, flat_rate: false }, true)).toBe(true);
    expect(offpeakPromptApplies({ configured: true }, true)).toBe(false);
    expect(offpeakPromptApplies({ configured: false, flat_rate: true }, true)).toBe(false);
    expect(offpeakPromptApplies({ configured: false }, false)).toBe(false);
    expect(offpeakPromptApplies(undefined, true)).toBe(false);
  });

  it("asks once, with growth's words, and a link to the off-peak settings", async () => {
    stubFetchRoutes(answer({}));
    view();
    expect(await screen.findByText("Do you have cheaper hours?")).toBeInTheDocument();
    expect(screen.getByText(/as an estimate from your own charges\./)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Set off-peak hours" })).toHaveAttribute("href", expect.stringContaining("/settings?tab=charging#offpeak"));
  });

  it("goes away on Not now, and stays away", async () => {
    stubFetchRoutes(answer({}));
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Not now" }));
    expect(screen.queryByText("Do you have cheaper hours?")).not.toBeInTheDocument();
    expect(window.localStorage.getItem(OFFPEAK_PROMPT_KEY)).toBe("1");
    cleanup();
    view();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText("Do you have cheaper hours?")).not.toBeInTheDocument();
  });

  it("is not shown once hours are saved, for a flat rate, or without a home rate", async () => {
    stubFetchRoutes(answer({ configured: true }));
    view();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText("Do you have cheaper hours?")).not.toBeInTheDocument();
    cleanup();
    stubFetchRoutes(answer({ flat_rate: true }));
    view();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText("Do you have cheaper hours?")).not.toBeInTheDocument();
    cleanup();
    stubFetchRoutes(answer({}));
    view(false);
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText("Do you have cheaper hours?")).not.toBeInTheDocument();
  });
});
