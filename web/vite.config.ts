import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import legacy from "@vitejs/plugin-legacy";

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    // Ensure the build stays Safari-15 compatible by compiling modern syntax.
    // renderLegacyChunks:false means we ship a single modern bundle; in
    // plugin-legacy v8 that bundle's syntax floor and polyfill set are
    // driven by modernTargets (it owns build.target now), not the vite
    // build.target field.
    legacy({
      modernTargets: ["safari >= 15", "ios_saf >= 15", "chrome >= 100", "firefox >= 100", "edge >= 100"],
      modernPolyfills: true,
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
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    sourcemap: false,
  },
});
