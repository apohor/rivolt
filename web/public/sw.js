// Rivolt service worker.
//
// Goals (deliberately minimal):
//   1. Offline app shell so the home-screen icon opens something
//      useful even when the Rivolt server is unreachable (Mac asleep,
//      bad Wi-Fi, off-LAN without VPN).
//   2. Never cache /api/* — shot data and live streams must always be
//      fresh. The SW explicitly does not intercept those requests.
//   3. Cheap cache-busting via CACHE_NAME: bumping the version string
//      invalidates every stored asset on next install.
//   4. Handle Web Push: the backend fans out notifications on
//      "shot finished" and "analysis ready"; we render them and route
//      taps back into the installed PWA.
//
// We intentionally avoid fancy precaching of hashed bundle assets. Vite
// already gives content-hashed filenames for /assets/*, so runtime
// stale-while-revalidate is enough and keeps this file trivial.

const CACHE_NAME = "rivolt-shell-v1";

// Files that make up the app shell. Everything else (hashed JS/CSS) is
// cached lazily on first fetch.
const SHELL_ASSETS = [
  "/",
  "/index.html",
  "/manifest.webmanifest",
  "/favicon.svg",
  "/apple-touch-icon.png",
  "/icon-192.png",
  "/icon-512.png",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.addAll(SHELL_ASSETS))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((k) => k !== CACHE_NAME)
            .map((k) => caches.delete(k)),
        ),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;

  // Only handle GETs. POST/DELETE etc. must always hit the network.
  if (req.method !== "GET") return;

  const url = new URL(req.url);

  // Never intercept cross-origin requests or API calls.
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/api/")) return;

  // For navigations (HTML) use network-first with cache fallback, so a
  // freshly-deployed shell is picked up immediately while offline still
  // works.
  if (req.mode === "navigate") {
    event.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone();
          caches.open(CACHE_NAME).then((c) => c.put("/index.html", copy));
          return res;
        })
        .catch(() =>
          caches.match("/index.html").then(
            (cached) =>
              cached ||
              new Response("Offline and no cached shell available.", {
                status: 503,
                statusText: "Service Unavailable",
                headers: { "Content-Type": "text/plain" },
              }),
          ),
        ),
    );
    return;
  }

  // Static assets (JS/CSS/images): cache-first, then revalidate in the
  // background. Vite's content hashes guarantee cache correctness.
  event.respondWith(
    caches.match(req).then((cached) => {
      const networkFetch = fetch(req)
        .then((res) => {
          if (res && res.ok && res.type === "basic") {
            const copy = res.clone();
            caches.open(CACHE_NAME).then((c) => c.put(req, copy));
          }
          return res;
        })
        .catch(() => cached);
      return cached || networkFetch;
    }),
  );
});

// --- Web Push --------------------------------------------------------------
//
// The backend delivers a JSON payload the shape of:
//   { title, body, tag?, url?, kind? }
// iOS Safari is notoriously picky about payloads: missing title or body
// crashes the notification silently, so we defensively supply defaults.

self.addEventListener("push", (event) => {
  let data = {};
  if (event.data) {
    try {
      data = event.data.json();
    } catch {
      data = { title: "Rivolt", body: event.data.text() };
    }
  }
  const title = data.title || "Rivolt";
  const body = data.body || "";
  const tag = data.tag || "rivolt";
  const url = data.url || "/";

  const opts = {
    body,
    tag,
    // Prevent the re-rendered notification from silently replacing a
    // still-unread one for the same tag: the user will see a subtle
    // re-notify instead.
    renotify: false,
    // Using the maskable 512 avoids iOS cropping the steam off the top.
    icon: "/icon-512.png",
    badge: "/icon-192.png",
    data: { url, kind: data.kind || "" },
  };

  event.waitUntil(self.registration.showNotification(title, opts));
});

// Tap behaviour: focus an already-open Rivolt window if we have one,
// navigating it to the target url if different. Otherwise open a fresh
// one. Matching on same-origin covers both the PWA (standalone) and the
// regular tab cases.
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = (event.notification.data && event.notification.data.url) || "/";
  event.waitUntil(
    (async () => {
      const all = await self.clients.matchAll({
        type: "window",
        includeUncontrolled: true,
      });
      for (const c of all) {
        // Navigate existing window and focus. Fall through to openWindow
        // if the client refuses navigation (older browsers, different
        // origin).
        try {
          if ("focus" in c) {
            await c.focus();
          }
          if ("navigate" in c && target) {
            await c.navigate(target);
          }
          return;
        } catch {
          /* ignore and try next */
        }
      }
      if (self.clients.openWindow) {
        await self.clients.openWindow(target);
      }
    })(),
  );
});
