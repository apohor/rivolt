import { describe, expect, it } from "vitest";
import { settingsAnchor } from "./settingsAnchor";

describe("settingsAnchor", () => {
  it("reads the fragment, as before", () => {
    expect(settingsAnchor("?tab=charging", "#offpeak")).toBe("offpeak");
    expect(settingsAnchor("", "#danger")).toBe("danger");
  });
  it("reads ?section=delete as the Danger zone, which survives a sign-in redirect", () => {
    expect(settingsAnchor("?tab=data&section=delete", "")).toBe("danger");
    expect(settingsAnchor("?section=delete&tab=data", "")).toBe("danger");
  });
  it("the fragment wins when both are there", () => {
    expect(settingsAnchor("?section=delete", "#export")).toBe("export");
  });
  it("ignores an unknown section and an empty hash", () => {
    expect(settingsAnchor("?section=nope", "")).toBe("");
    expect(settingsAnchor("?section=__proto__", "")).toBe("");
    expect(settingsAnchor("", "#")).toBe("");
    expect(settingsAnchor("", "")).toBe("");
  });
});
