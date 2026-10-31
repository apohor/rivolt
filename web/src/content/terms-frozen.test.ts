import { createHash } from "node:crypto";
import { describe, expect, it } from "vitest";
import frozen from "./terms-2026-10-23.md?raw";

// terms-2026-10-23.md is the exact text accounts created before the new Terms accepted, served at /terms/2026-10-23. It must NEVER change
// when /terms does (existing accounts stay on these Terms until the new ones take effect for them). If this fails, restore the file from
// git (git show f54a4f55:web/src/content/terms.md) rather than updating the hash.
describe("the October 23, 2026 Terms are frozen", () => {
  it("has the exact text, byte for byte", () => {
    expect(createHash("sha256").update(frozen).digest("hex")).toBe("aa51fb3e4fd68953243056fabb024478fa5958e2ba7994347984cde617a36547");
  });

  it("is the Terms of that date", () => {
    expect(frozen).toMatch(/^# Rivolt Terms of Service\n\nLast updated: October 23, 2026\n/);
    expect(frozen).toContain("## 5. Free service and beta");
  });
});
