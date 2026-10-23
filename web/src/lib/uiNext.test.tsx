import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { useApplyUiNext } from "./uiNext";

// The new look is the only one: a shell always marks html[data-ui="next"] (its CSS layer).
function Shell() {
  useApplyUiNext();
  return null;
}

describe("useApplyUiNext", () => {
  afterEach(cleanup);
  it("marks the page for the look's CSS while the shell is mounted", () => {
    const r = render(<Shell />);
    expect(document.documentElement.getAttribute("data-ui")).toBe("next");
    r.unmount();
    expect(document.documentElement.getAttribute("data-ui")).toBeNull();
  });
});
