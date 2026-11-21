import { readFileSync } from "node:fs";
import { join } from "node:path";
import { vi } from "vitest";

// stubLegalFetch answers a fetch of a src/content/*.md asset (what a page imported with ?url asks for) with that file, so a legal page renders
// the real text. Anything else 404s.
export function stubLegalFetch() {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const name = String(input).split("?")[0].split("/").pop() ?? "";
    try {
      const body = readFileSync(join(__dirname, "..", "content", name.replace(/-[A-Za-z0-9_]{8}(\.md)$/, "$1")), "utf8");
      return { ok: true, status: 200, text: async () => body } as unknown as Response;
    } catch {
      return { ok: false, status: 404, text: async () => "" } as unknown as Response;
    }
  });
  vi.stubGlobal("fetch", fn);
  return fn;
}
