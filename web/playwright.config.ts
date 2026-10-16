import { defineConfig } from "@playwright/test";

// Several worktrees can run the harness at once; each needs its own
// preview port, or one run silently tests another worktree's build.
const PORT = Number(process.env.E2E_PORT || 4173);

// Smoke + screenshot harness. Serves the built SPA with `vite preview`
// and stubs every /api/* call in the specs (see e2e/stub.ts), so it
// needs no backend and no real data. Chromium only.
export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.pw.ts",
  fullyParallel: true,
  // CI: one retry (a flaky pass shows as "flaky" in the report), two workers
  // per shard, and an HTML report to upload next to the screenshots.
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? [["list"], ["html", { open: "never", outputFolder: "playwright-report" }]] : [["list"]],
  outputDir: "./e2e/__results__",
  use: { baseURL: `http://127.0.0.1:${PORT}` },
  webServer: {
    command: `npx vite preview --host 127.0.0.1 --port ${PORT} --strictPort`,
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: true,
    timeout: 60_000,
  },
});
