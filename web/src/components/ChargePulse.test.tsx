import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { chargePulseState, PulseBolt, withGlow } from "./ChargePulse";

afterEach(cleanup);

describe("chargePulseState", () => {
  it("charging while power flows, plugged when the cable is in and nothing flows, else none", () => {
    expect(chargePulseState(true, 11, true)).toBe("charging");
    expect(chargePulseState(true, 0.1, false)).toBe("charging");
    expect(chargePulseState(true, 0, true)).toBe("plugged");
    expect(chargePulseState(true, null, true)).toBe("plugged");
    expect(chargePulseState(true, 0, false)).toBe("none");
    expect(chargePulseState(true, undefined, false)).toBe("none");
  });
  it("key off: none in every state", () => {
    for (const kw of [null, 0, 11]) for (const plugged of [true, false]) expect(chargePulseState(false, kw, plugged)).toBe("none");
  });
});

describe("markup", () => {
  it("none draws nothing and the bar stays as it is", () => {
    const { container } = render(<div>{withGlow("none", 50, <i data-testid="bar" />)}<PulseBolt state="none" /></div>);
    expect(container.innerHTML).toBe('<div><i data-testid="bar"></i></div>');
  });
  it("the plugged badge has no glow; charging adds the glow over the bar and a bolt hidden from screen readers", () => {
    const { container } = render(<div>{withGlow("plugged", 50, <i data-testid="bar" />)}<PulseBolt state="plugged" /></div>);
    expect(screen.queryByTestId("charge-pulse-glow")).toBeNull();
    expect(screen.getByTestId("charge-pulse-bolt").getAttribute("data-pulse")).toBe("plugged");
    cleanup();
    render(<div>{withGlow("charging", 140, <i data-testid="bar" />)}<PulseBolt state="charging" /></div>);
    expect((screen.getByTestId("charge-pulse-glow") as HTMLElement).style.left).toBe("100%");
    expect(screen.getByTestId("charge-pulse-bolt").getAttribute("aria-hidden")).toBe("true");
    expect(container).toBeTruthy();
  });
});
