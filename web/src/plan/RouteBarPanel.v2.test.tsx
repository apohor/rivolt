import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { RouteBar } from "./RouteBarPanel";

let on = true;
vi.mock("../lib/config", () => ({ useDesignPreview: () => on }));
afterEach(cleanup);

function setup(moreCount: number) {
  render(
    <RouteBar
      from={<input aria-label="From field" />}
      to={<input aria-label="To field" />}
      onSwap={() => {}}
      when={<button type="button">Now</button>}
      soc={{ value: "", live: 62, onChange: () => {} }}
      moreCount={moreCount}
      more={<span />}
      submit={<div data-plan-submit><button type="submit">Plan trip</button></div>}
    />,
  );
}

describe("RouteBar badge with plan_v2", () => {
  it("says what the number is when the key is on", () => {
    on = true;
    setup(2);
    expect(screen.getByRole("button", { name: /More options/ })).toHaveTextContent("2 changed");
  });
  it("is the bare number with the key off", () => {
    on = false;
    setup(2);
    const t = screen.getByRole("button", { name: /More options/ }).textContent;
    expect(t).toBe("More options2");
  });
});
