import { describe, expect, it } from "vitest";
import { appButtonLabel, appLinkKind, shareFooterLine } from "./appLink";

describe("the iPhone app link", () => {
  it("tells an App Store link from a TestFlight one, and refuses anything else", () => {
    expect(appLinkKind("https://apps.apple.com/us/app/rivolt/id1234567890")).toBe("store");
    expect(appLinkKind("https://testflight.apple.com/join/abc123")).toBe("testflight");
    for (const bad of ["", undefined, null, "http://apps.apple.com/app/id1", "https://evil.example/apps.apple.com", "https://apps.apple.com.evil.example/x", "https://u:p@apps.apple.com/x", "not a url"]) {
      expect(appLinkKind(bad as string | undefined | null)).toBeNull();
    }
  });
  it("labels the button for the kind of link", () => {
    expect(appButtonLabel("store")).toBe("Open in the App Store");
    expect(appButtonLabel("testflight")).toBe("Join the TestFlight beta");
  });
  it("puts the app on a share card for an App Store link only", () => {
    expect(shareFooterLine("https://apps.apple.com/us/app/rivolt/id1234567890")).toBe("Made with Rivolt · rivolt.dev · iPhone app on the App Store");
    expect(shareFooterLine("https://testflight.apple.com/join/abc123")).toBe("Made with Rivolt · rivolt.dev");
    expect(shareFooterLine("")).toBe("Made with Rivolt · rivolt.dev");
    expect(shareFooterLine(undefined)).toBe("Made with Rivolt · rivolt.dev");
  });
});
