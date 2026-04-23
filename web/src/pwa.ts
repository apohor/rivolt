// Service worker registration.
//
// Registers the SW at /sw.js on load, but only when served over HTTPS
// (or localhost). We deliberately keep this separate from main.tsx so
// any registration failure can't take down the app.
//
// The SW itself is hand-written (no Workbox) and kept in public/sw.js
// so Vite copies it to the dist root without transformation.

export function registerServiceWorker() {
  if (typeof window === "undefined") return;
  if (!("serviceWorker" in navigator)) return;

  // SW only installs on a secure context. Safari on localhost counts
  // as secure; everything else must be HTTPS. This avoids confusing
  // "Registration failed: insecure origin" errors on a LAN HTTP setup.
  const isSecure =
    window.isSecureContext ||
    window.location.hostname === "localhost" ||
    window.location.hostname === "127.0.0.1";
  if (!isSecure) return;

  // Register after the window load event so SW install doesn't compete
  // with the initial app bundle for bandwidth / CPU.
  window.addEventListener("load", () => {
    navigator.serviceWorker
      .register("/sw.js", { scope: "/" })
      .catch((err) => {
        // Swallow — SW is a progressive enhancement, not a hard dep.
        console.warn("[caffeine] service worker registration failed:", err);
      });
  });
}
