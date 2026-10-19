import { describe, expect, it } from "vitest";
import { escapeHTML } from "./escapeHtml";

describe("escapeHTML", () => {
  it("neutralises markup, attribute breakouts and script payloads", () => {
    expect(escapeHTML(`<img src=x onerror="alert(1)">`)).toBe("&lt;img src=x onerror=&quot;alert(1)&quot;&gt;");
    expect(escapeHTML(`' onmouseover='alert(1)`)).toBe("&#39; onmouseover=&#39;alert(1)");
    expect(escapeHTML("<script>alert(1)</script>")).not.toContain("<");
    expect(escapeHTML("Tom & Jerry's")).toBe("Tom &amp; Jerry&#39;s");
  });
  it("leaves plain text alone", () => {
    expect(escapeHTML("Harris Ranch, CA")).toBe("Harris Ranch, CA");
  });
});
