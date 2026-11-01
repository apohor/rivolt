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
  // CI: one retry (a flaky pass shows as "flaky" in the report and the job summary), E2E_WORKERS
  // workers per shard (default four, the runner's four vCPUs), an HTML report to upload next to the
  // screenshots, and a JSON report (e2e-results/results.json) for the job summary's flaky count and
  // the per-spec timings that balance the shards (scripts/e2e-timings.mjs, scripts/e2e-shard.mjs).
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? Number(process.env.E2E_WORKERS || 4) : undefined,
  reporter: process.env.CI
    ? [["list"], ["html", { open: "never", outputFolder: "playwright-report" }], ["json", { outputFile: "e2e-results/results.json" }]]
    : [["list"]],
  outputDir: "./e2e/__results__",
  use: { baseURL: `http://127.0.0.1:${PORT}` },
  // Chromium runs everything. WebKit (the engine behind iPhone Safari, where
  // users are) runs the phone specs only, and only where it is installed:
  // CI sets E2E_WEBKIT=1; a laptop without the WebKit download is not asked
  // to run it. Select one with --project=chromium|webkit.
  projects: [
    { name: "chromium", use: { browserName: "chromium" } },
    ...(process.env.E2E_WEBKIT ? [{ name: "webkit", use: { browserName: "webkit" as const }, grep: /iphone|phone|@webkit/i }] : []),
  ],
  webServer: {
    command: `npx vite preview --host 127.0.0.1 --port ${PORT} --strictPort`,
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: true,
    timeout: 60_000,
  },
});
