import { createHash } from "node:crypto";
import { describe, expect, it } from "vitest";
import frozen from "./terms-2026-10-23.md?raw";
import frozen1117 from "./terms-2026-11-17.md?raw";

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

// terms-2026-11-17.md is the Terms as published on launch day (before "beta" came out of section 5), served at /terms/2026-11-17.
describe("the November 17, 2026 Terms are frozen", () => {
  it("has the exact text, byte for byte", () => {
    expect(createHash("sha256").update(frozen1117).digest("hex")).toBe("9ffa89fc6c003b12e45f7de7e5916c95ab081d4488cd7a0db22dab9aef7026af");
  });

  it("is the Terms of that date", () => {
    expect(frozen1117).toMatch(/^# Rivolt Terms of Service\n\nLast updated: November 17, 2026\n/);
  });
});
