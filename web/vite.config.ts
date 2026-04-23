import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import legacy from "@vitejs/plugin-legacy";

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    // Ensure the build stays Safari-15 compatible by compiling modern syntax.
    legacy({
      targets: ["safari >= 15", "ios_saf >= 15"],
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
    target: ["safari15", "ios15", "chrome100"],
    sourcemap: false,
  },
});
