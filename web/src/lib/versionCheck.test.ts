import { describe, expect, it } from "vitest";
import { baselineVersion, isNewVersion, isPreviewHost, nextAction } from "./versionCheck";

describe("baselineVersion", () => {
  it("prefers the baked version, ignoring the dev stamp", () => {
    expect(baselineVersion("v1.2.3", "v9")).toBe("v1.2.3");
    expect(baselineVersion("dev", "v9")).toBe("v9");
    expect(baselineVersion(undefined, "v9")).toBe("v9");
    expect(baselineVersion("", null)).toBeNull();
  });
});

describe("isNewVersion", () => {
  it("only differs when both are known", () => {
    expect(isNewVersion("a", "b")).toBe(true);
    expect(isNewVersion("a", "a")).toBe(false);
    expect(isNewVersion(null, "b")).toBe(false);
    expect(isNewVersion("a", null)).toBe(false);
    expect(isNewVersion("a", "")).toBe(false);
  });
});

describe("nextAction", () => {
  const base = { baseline: "a", live: "b" };
  it("prod: prompts, never reloads, hidden or not", () => {
    expect(nextAction({ ...base, hidden: false, preview: false })).toBe("prompt");
    expect(nextAction({ ...base, hidden: true, preview: false })).toBe("prompt");
  });
  it("preview: prompts while visible, reloads once hidden", () => {
    expect(nextAction({ ...base, hidden: false, preview: true })).toBe("prompt");
    expect(nextAction({ ...base, hidden: true, preview: true })).toBe("reload");
  });
  it("does nothing when versions match", () => {
    expect(nextAction({ baseline: "a", live: "a", hidden: true, preview: true })).toBe("none");
  });
});

describe("isPreviewHost", () => {
  it("matches only preview.rivolt.dev", () => {
    expect(isPreviewHost("preview.rivolt.dev")).toBe(true);
    expect(isPreviewHost("rivolt.dev")).toBe(false);
    expect(isPreviewHost("evil-preview.rivolt.dev")).toBe(false);
    expect(isPreviewHost("localhost")).toBe(false);
  });
});
