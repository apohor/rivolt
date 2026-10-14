import { defineConfig } from "@playwright/test";

// Smoke + screenshot harness. Serves the built SPA with `vite preview`
// and stubs every /api/* call in the specs (see e2e/stub.ts), so it
// needs no backend and no real data. Chromium only.
export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.pw.ts",
  fullyParallel: true,
  retries: 0,
  reporter: [["list"]],
  outputDir: "./e2e/__results__",
  use: { baseURL: "http://127.0.0.1:4173" },
  webServer: {
    command: "npx vite preview --host 127.0.0.1 --port 4173 --strictPort",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: true,
    timeout: 60_000,
  },
});
