import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { LocationOverrideToggle, PlaceOverrideControl } from "./ChargeDetailPage";
import { nearestOwnNetwork, parseNetworkLocation } from "../lib/networkLocation";
import type { ReactNode } from "react";

// #906: the per-charge overrides and the user's own network locations (synthetic positions).

function mockFetch() {
  const calls: { method: string; url: string; body: string }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ method: init?.method ?? "GET", url: String(input), body: String(init?.body ?? "") });
      return { ok: true, status: 204, headers: { get: () => null }, text: async () => "" } as unknown as Response;
    }),
  );
  return calls;
}

function wrap(node: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{node}</QueryClientProvider>);
}

describe("charge overrides", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("sets and clears the place", async () => {
    const calls = mockFetch();
    wrap(<PlaceOverrideControl charge={{ ID: "c 1", PlaceOverride: "" }} />);
    expect(screen.getByRole("button", { name: "Auto" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("From your home location.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Home" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({ method: "PUT", url: "/api/charges/c%201/place", body: '{"place":"home"}' });
    cleanup();

    wrap(<PlaceOverrideControl charge={{ ID: "c1", PlaceOverride: "away" }} />);
    expect(screen.getByText(/Set by you/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Away" })); // already set: no call
    fireEvent.click(screen.getByRole("button", { name: "Auto" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1]).toMatchObject({ method: "PUT", url: "/api/charges/c1/place", body: '{"place":null}' });
  });

  it("pins where the truck was", async () => {
    const calls = mockFetch();
    wrap(<LocationOverrideToggle charge={{ ID: "c1", LocationOverride: "" }} />);
    const box = screen.getByRole("checkbox", { name: /Pin where the truck was/ });
    expect(box).not.toBeChecked();
    expect(screen.queryByText("set by you")).toBeNull();
    fireEvent.click(box);
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({ method: "PUT", url: "/api/charges/c1/location", body: '{"recorded":true}' });
    cleanup();
    wrap(<LocationOverrideToggle charge={{ ID: "c1", LocationOverride: "recorded" }} />);
    expect(screen.getByRole("checkbox")).toBeChecked();
    expect(screen.getByText("set by you")).toBeInTheDocument();
  });
});

describe("charge overrides on a failed save", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows the choice at once and puts the stored one back when the save fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: false, status: 500, headers: { get: () => "text/plain" }, text: async () => "boom" }) as unknown as Response),
    );
    wrap(
      <>
        <PlaceOverrideControl charge={{ ID: "c1", PlaceOverride: "" }} />
        <LocationOverrideToggle charge={{ ID: "c1", LocationOverride: "" }} />
      </>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Away" }));
    expect(screen.getByRole("button", { name: "Away" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.getByRole("checkbox")).toBeChecked();
    await waitFor(() => expect(screen.getAllByRole("alert")).toHaveLength(2));
    expect(screen.getByRole("button", { name: "Auto" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("checkbox")).not.toBeChecked();
  });
});

describe("own network locations", () => {
  const nets = [
    { name: "Office", price_per_kwh: 0.2, currency: "USD", lat: 40.0, lon: -100.0 },
    { name: "Depot", price_per_kwh: 0.3, currency: "USD", lat: 40.0005, lon: -100.0 },
    { name: "Built-in", slug: "evgo", price_per_kwh: 0.5, currency: "USD", lat: 40.0, lon: -100.0 },
    { name: "Plain", price_per_kwh: 0.3, currency: "USD" },
  ];

  it("labels a charge within 150 m with the nearest of the user's own rows", () => {
    expect(nearestOwnNetwork(nets, 40.0004, -100.0)?.name).toBe("Depot"); // ~11 m vs ~44 m
    expect(nearestOwnNetwork(nets, 40.0, -100.0)?.name).toBe("Office");
    expect(nearestOwnNetwork(nets, 40.002, -100.0)).toBeNull(); // ~170 m from Depot
    expect(nearestOwnNetwork(nets.slice(2), 40.0, -100.0)).toBeNull(); // a built-in row never labels
    expect(nearestOwnNetwork(nets, 0, 0)).toBeNull();
    expect(nearestOwnNetwork(undefined, 40, -100)).toBeNull();
  });

  it("parses the Settings field like the server validates it", () => {
    expect(parseNetworkLocation("")).toEqual({ ok: true });
    expect(parseNetworkLocation(" 40.5, -100.25 ")).toEqual({ ok: true, lat: 40.5, lon: -100.25 });
    expect(parseNetworkLocation("40.5 -100.25")).toEqual({ ok: true, lat: 40.5, lon: -100.25 });
    for (const bad of ["40.5", "91, 0", "0, 181", "0, 0", "a, b", "1, 2, 3"]) {
      expect(parseNetworkLocation(bad).ok).toBe(false);
    }
  });
});
