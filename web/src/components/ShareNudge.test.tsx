import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { ShareNudge, SHARE_NUDGE_KEY, SHARE_NUDGE_BODY, SHARE_NUDGE_TITLE, readShareNudgeDismissed, rememberShareNudgeDismissed, shareNudgeShows } from "./ShareNudge";

afterEach(() => {
  cleanup();
  window.localStorage.clear();
});

describe("the one-time share nudge", () => {
  const base = { sample: false, disabled: false, prompt: false, dismissed: false };
  it("shows once, with something to share, outside the demo and the email's own prompt", () => {
    expect(shareNudgeShows(base)).toBe(true);
    expect(shareNudgeShows({ ...base, sample: true })).toBe(false);
    expect(shareNudgeShows({ ...base, disabled: true })).toBe(false);
    expect(shareNudgeShows({ ...base, prompt: true })).toBe(false);
    expect(shareNudgeShows({ ...base, dismissed: true })).toBe(false);
  });
  it("remembers a dismissal on this device, and survives blocked storage", () => {
    expect(readShareNudgeDismissed()).toBe(false);
    rememberShareNudgeDismissed();
    expect(window.localStorage.getItem(SHARE_NUDGE_KEY)).toBe("1");
    expect(readShareNudgeDismissed()).toBe(true);
  });
  it("says what the card holds and that nothing is posted, and its buttons call back", () => {
    let shared = 0;
    let later = 0;
    render(<ShareNudge onShare={() => shared++} onLater={() => later++} />);
    expect(SHARE_NUDGE_TITLE).toBe("Share what charging cost this month");
    expect(SHARE_NUDGE_BODY).toBe("Your statement card shows the month's kWh and miles, with cost left out unless you add it. Nothing is posted; you pick where it goes.");
    // No figures in the nudge itself, and no comparing (DA).
    expect(SHARE_NUDGE_BODY + SHARE_NUDGE_TITLE).not.toMatch(/\$|\d|compare/i);
    fireEvent.click(screen.getByRole("button", { name: "Share statement" }));
    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect([shared, later]).toEqual([1, 1]);
  });
});
