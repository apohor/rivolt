import { afterEach, describe, expect, it } from "vitest";
import { setServerUiNext, uiNextEnabled } from "./uiNext";

// ui_theme_next: off by default; the server flag turns it on; a per-device
// override (?ui=next / ?ui=off) beats the server flag.
function at(search: string) {
  window.history.replaceState(null, "", `/${search}`);
}

describe("ui_theme_next resolution", () => {
  afterEach(() => {
    setServerUiNext(false);
    window.localStorage.clear();
    at("");
  });

  it("is off by default", () => {
    expect(uiNextEnabled()).toBe(false);
  });
  it("follows the server flag", () => {
    setServerUiNext(true);
    expect(uiNextEnabled()).toBe(true);
  });
  it("?ui=next turns it on and persists; ?ui=off beats the server flag; ?ui=reset clears", () => {
    at("?ui=next");
    expect(uiNextEnabled()).toBe(true);
    at("");
    expect(uiNextEnabled()).toBe(true); // persisted
    setServerUiNext(true);
    at("?ui=off");
    expect(uiNextEnabled()).toBe(false);
    at("?ui=reset");
    expect(uiNextEnabled()).toBe(true); // back to the server flag
  });
});
