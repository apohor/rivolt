import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { revealMode, revealStop } from "./reveal";
import { StopCard } from "./StopCard";

afterEach(() => {
  cleanup();
  delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("revealMode", () => {
  it("moves a card that fits the least, and aligns the title of one that does not", () => {
    expect(revealMode(200, 400)).toBe("card-nearest");
    expect(revealMode(400, 400)).toBe("card-nearest");
    expect(revealMode(401, 400)).toBe("title-top");
  });
});

function sheetWith(cardH: number, scrollerH: number) {
  const body = document.createElement("div");
  body.setAttribute("data-plan-sheet-body", "");
  Object.defineProperty(body, "clientHeight", { value: scrollerH });
  const card = document.createElement("li");
  card.getBoundingClientRect = () => ({ height: cardH }) as DOMRect;
  const title = document.createElement("button");
  card.append(title);
  body.append(card);
  document.body.append(body);
  card.scrollIntoView = vi.fn();
  title.scrollIntoView = vi.fn();
  return { card, title };
}

describe("revealStop", () => {
  it("in the sheet: a card that fits is revealed with nearest, smoothly", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    const { card, title } = sheetWith(150, 300);
    revealStop(card, title);
    expect(card.scrollIntoView).toHaveBeenCalledWith({ block: "nearest", behavior: "smooth" });
    expect(title.scrollIntoView).not.toHaveBeenCalled();
  });
  it("in the sheet: a card taller than the scroller puts its TITLE at the top", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    const { card, title } = sheetWith(500, 300);
    revealStop(card, title);
    expect(title.scrollIntoView).toHaveBeenCalledWith({ block: "start", behavior: "smooth" });
    expect(card.scrollIntoView).not.toHaveBeenCalled();
  });
  it("respects prefers-reduced-motion: no smooth scrolling", () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce") }));
    const { card, title } = sheetWith(150, 300);
    revealStop(card, title);
    expect(card.scrollIntoView).toHaveBeenCalledWith({ block: "nearest", behavior: "auto" });
  });
  it("outside a sheet (the page scrolls): the card, nearest", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    const card = document.createElement("li");
    const title = document.createElement("button");
    card.append(title);
    document.body.append(card);
    card.scrollIntoView = vi.fn();
    revealStop(card, title);
    expect(card.scrollIntoView).toHaveBeenCalledWith({ block: "nearest", behavior: "smooth" });
  });
  it("is safe where scrollIntoView does not exist", () => {
    const card = document.createElement("li");
    const title = document.createElement("button");
    card.append(title);
    expect(() => revealStop(card, title)).not.toThrow();
  });
});

describe("StopCard reveals itself when it expands", () => {
  const w = {
    Name: "Sample Fast Charge", Latitude: 35, Longitude: -117, ArrivalSoC: 20, DepartureSoC: 80, ChargeDurationSec: 1500,
    ArrivalTimeUTC: "2026-11-14T16:00:00Z", DepartureTimeUTC: "2026-11-14T16:25:00Z", MaxPowerKW: 200, DistanceFromOriginMeters: 100000,
  } as never;
  const props = { kind: "stop" as const, name: "Barstow", waypoint: w, currency: "USD", timeShiftMs: 0, showLeg: false, onSelect: () => {} };

  it("scrolls on the collapsed → expanded transition only", () => {
    // jsdom has no scrollIntoView: give every element one we can count.
    const spy = vi.fn();
    Object.defineProperty(Element.prototype, "scrollIntoView", { value: spy, configurable: true, writable: true });
    const calls = () => spy.mock.calls.length;
    const { rerender } = render(<ol><StopCard {...props} selected={false} /></ol>);
    expect(calls()).toBe(0);
    rerender(<ol><StopCard {...props} selected /></ol>);
    expect(calls()).toBe(1);
    rerender(<ol><StopCard {...props} selected /></ol>); // still open: no re-scroll
    expect(calls()).toBe(1);
    rerender(<ol><StopCard {...props} selected={false} /></ol>); // collapsing does not scroll
    expect(calls()).toBe(1);
  });
});
