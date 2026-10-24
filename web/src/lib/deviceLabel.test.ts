import { describe, expect, it } from "vitest";
import { activeAgo, deviceName } from "./deviceLabel";

describe("deviceName", () => {
  it("uses the stored label as it is", () => {
    expect(deviceName({ kind: "app", device_label: "iPhone", user_agent: "Rivolt/1" })).toBe("iPhone");
    expect(deviceName({ kind: "web", device_label: "  Chrome on macOS  " })).toBe("Chrome on macOS");
  });
  it("names an app sign-in with no label by its kind", () => {
    expect(deviceName({ kind: "app", device_label: "" })).toBe("iPhone app");
  });
  it("makes a browser name from the User-Agent, nothing finer", () => {
    const chromeMac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36";
    const safariIphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1";
    const edgeWin = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0";
    const firefoxLinux = "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0";
    expect(deviceName({ kind: "web", device_label: "", user_agent: chromeMac })).toBe("Chrome on macOS");
    expect(deviceName({ kind: "web", device_label: "", user_agent: safariIphone })).toBe("Safari on iOS");
    expect(deviceName({ kind: "web", device_label: "", user_agent: edgeWin })).toBe("Edge on Windows");
    expect(deviceName({ kind: "web", device_label: "", user_agent: firefoxLinux })).toBe("Firefox on Linux");
  });
  it("falls back to a plain word, never the raw User-Agent", () => {
    expect(deviceName({ kind: "web", device_label: "" })).toBe("Browser");
    expect(deviceName({ kind: "web", device_label: "", user_agent: "curl/8.4.0" })).toBe("Browser");
  });
});

describe("activeAgo", () => {
  const now = new Date("2026-10-24T12:00:00Z");
  it("reads as a coarse age", () => {
    expect(activeAgo("2026-10-24T11:59:30Z", now)).toBe("just now");
    expect(activeAgo("2026-10-24T11:45:00Z", now)).toBe("15 min ago");
    expect(activeAgo("2026-10-24T07:00:00Z", now)).toBe("5 h ago");
    expect(activeAgo("2026-10-20T12:00:00Z", now)).toBe("4 days ago");
    expect(activeAgo("not a date", now)).toBe("");
  });
});
