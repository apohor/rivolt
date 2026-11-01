import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import legacy from "@vitejs/plugin-legacy";

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    // Compile modern syntax down to the browserslist floor. Tailwind v4
    // generates @property / color-mix() CSS that needs Safari 16.4+, so the
    // floor is 16.4 (was 15). renderLegacyChunks:false means we ship a single
    // modern bundle; in plugin-legacy v8 that bundle's syntax floor and
    // polyfill set are driven by modernTargets (it owns build.target now),
    // not the vite build.target field.
    //
    // No polyfills: modernPolyfills used to be true, which injects core-js for every feature the floor lacks (Iterator helpers, Set
    // methods, getOrInsert, ArrayBuffer.transfer, JSON.parse source: a 20.8 KB chunk) whether or not any code calls it, and none did.
    // scripts/check-floor-apis.mjs, run after the build in CI, fails when the shipped JS starts using an API newer than the floor.
    legacy({
      modernTargets: ["safari >= 16.4", "ios_saf >= 16.4", "chrome >= 111", "firefox >= 128", "edge >= 111"],
      renderLegacyChunks: false,
    }),
  ],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: false,
        ws: true,
        // Swallow benign socket resets from the WebSocket proxy. The
        // browser closing a live-stream tab or the Go backend tearing
        // down its upstream to the machine both surface as ECONNRESET
        // here, which http-proxy otherwise logs as a scary stack trace
        // on every navigation. Any other error still prints.
        configure: (proxy) => {
          proxy.on("error", (err) => {
            const code = (err as NodeJS.ErrnoException).code;
            if (code === "ECONNRESET" || code === "EPIPE") return;
            console.error("[vite proxy]", err.message);
          });
        },
      },
    },
  },
  build: {
    // Everything under assets/ must be Vite's content-hashed output: the
    // server caches assets/<name>-<8-char hash>.<ext> for a year (spa.go,
    // #797). Never put unhashed files there; use web/public instead.
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    sourcemap: false,
  },
});
