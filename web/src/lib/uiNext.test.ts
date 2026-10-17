import { afterEach, describe, expect, it } from "vitest";
import { overrideAllowed, setServerUiNext, uiNextEnabled } from "./uiNext";

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

  it("honours the override only on preview and local dev", () => {
    expect(overrideAllowed("preview.rivolt.dev")).toBe(true);
    expect(overrideAllowed("localhost")).toBe(true);
    expect(overrideAllowed("127.0.0.1")).toBe(true);
    expect(overrideAllowed("rivolt.dev")).toBe(false);
    expect(overrideAllowed("demo.rivolt.dev")).toBe(false);
    expect(overrideAllowed("evil-preview.rivolt.dev")).toBe(false);
    expect(overrideAllowed("preview.example.com")).toBe(false); // a self-hoster's host
  });

  it("on prod, ?ui=next is ignored and a stored override is cleared; the server flag still works", () => {
    window.localStorage.setItem("rivolt.ui.next", "on");
    at("?ui=next");
    expect(uiNextEnabled("rivolt.dev")).toBe(false);
    expect(window.localStorage.getItem("rivolt.ui.next")).toBeNull();
    setServerUiNext(true);
    expect(uiNextEnabled("rivolt.dev")).toBe(true);
  });
});
