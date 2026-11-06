import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChargingNetwork } from "../lib/api";

const { getChargingNetworks, setChargingNetworks } = vi.hoisted(() => ({ getChargingNetworks: vi.fn(), setChargingNetworks: vi.fn() }));
vi.mock("../lib/api", async (orig) => ({ ...(await orig<typeof import("../lib/api")>()), backend: { getChargingNetworks, setChargingNetworks } }));
import { listedNetworks, NETWORKS_HONESTY, PlannerNetworks, preferredChanged } from "./PlannerNetworks";

const net = (name: string, slug: string | undefined, preferred?: boolean): ChargingNetwork => ({ name, price_per_kwh: 0, currency: "USD", slug, preferred }) as ChargingNetwork;

describe("listedNetworks / preferredChanged", () => {
  it("lists only built-in networks, preferred first, then by name", () => {
    const rows = [net("Zeta", "z"), net("My own", undefined), net("Alpha", "a"), net("Mid", "m", true)];
    expect(listedNetworks(rows).map((i) => rows[i].name)).toEqual(["Mid", "Alpha", "Zeta"]);
  });
  it("only a changed flag is a change (undefined and false are the same)", () => {
    const a = [net("A", "a", false), net("B", "b")];
    expect(preferredChanged(a, a)).toBe(false);
    expect(preferredChanged([net("A", "a", false), net("B", "b", false)], a)).toBe(false);
    expect(preferredChanged([net("A", "a", true), net("B", "b")], a)).toBe(true);
  });
});

describe("PlannerNetworks", () => {
  afterEach(() => { cleanup(); getChargingNetworks.mockReset(); setChargingNetworks.mockReset(); });

  it("saves the same list through the existing setting and then plans again", async () => {
    const rows = [net("Alpha", "a"), net("Beta", "b")];
    getChargingNetworks.mockResolvedValue(rows);
    setChargingNetworks.mockImplementation(async (r: ChargingNetwork[]) => r);
    const onSaved = vi.fn();
    render(<QueryClientProvider client={new QueryClient()}><PlannerNetworks onSaved={onSaved} /></QueryClientProvider>);
    const box = await screen.findByRole("checkbox", { name: /Alpha/ });
    expect(screen.getByText(NETWORKS_HONESTY)).toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Save and plan again" });
    expect(button).toBeDisabled();
    fireEvent.click(box);
    expect(button).toBeEnabled();
    fireEvent.click(button);
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(setChargingNetworks.mock.calls[0][0].map((r: ChargingNetwork) => [r.slug, !!r.preferred])).toEqual([["a", true], ["b", false]]);
  });

  it("promises nothing", () => {
    expect(NETWORKS_HONESTY).toContain("not guaranteed");
    expect(NETWORKS_HONESTY).not.toMatch(/will use|always|guarantee[ds]? (to|you)/i);
  });

  it("shows nothing when the list cannot be read or has no built-in network", async () => {
    getChargingNetworks.mockResolvedValue([net("Mine", undefined)]);
    const { container } = render(<QueryClientProvider client={new QueryClient()}><PlannerNetworks /></QueryClientProvider>);
    await waitFor(() => expect(getChargingNetworks).toHaveBeenCalled());
    expect(container.querySelector("[data-planner-networks]")).toBeNull();
  });
});
