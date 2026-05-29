import { useEffect, useState } from "react";

const DISMISS_KEY = "rivolt:ios-install-banner:dismissed";

// IOSInstallBanner nudges iPhone Safari users to add Rivolt to the
// home screen. Web Push on iOS requires the PWA to be installed
// first — without it, the Notifications panel silently fails. The
// banner shows once per session/dismiss; tapping X writes a flag to
// localStorage so it doesn't bother the user again.
//
// Hidden when:
//   - not iOS Safari (other platforms don't have the requirement)
//   - already running in standalone PWA mode (navigator.standalone)
//   - user dismissed it previously
export default function IOSInstallBanner() {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    if (typeof window === "undefined") return;
    // iOS Safari detection. iPadOS 13+ reports as MacIntel + touch,
    // so include that path too.
    const ua = navigator.userAgent;
    const isIPhone = /iPhone|iPod/i.test(ua);
    const isIPad =
      /iPad/i.test(ua) ||
      (navigator.platform === "MacIntel" && (navigator as Navigator & { maxTouchPoints?: number }).maxTouchPoints! > 1);
    if (!isIPhone && !isIPad) return;
    // Already installed?
    const nav = navigator as Navigator & { standalone?: boolean };
    if (nav.standalone) return;
    // matchMedia is the modern way; fallback is the legacy
    // navigator.standalone above (which is iOS-specific).
    if (window.matchMedia && window.matchMedia("(display-mode: standalone)").matches) return;
    // User dismissed?
    try {
      if (localStorage.getItem(DISMISS_KEY) === "1") return;
    } catch {
      /* private-mode storage; ignore */
    }
    setVisible(true);
  }, []);

  if (!visible) return null;

  return (
    <div className="sticky top-0 z-1200 border-b border-emerald-900/60 bg-emerald-950/80 backdrop-blur-sm">
      <div className="mx-auto flex max-w-5xl items-center gap-3 px-4 py-2 text-xs text-emerald-100">
        <span aria-hidden>📲</span>
        <span className="flex-1 leading-snug">
          For push notifications and a real app feel, tap{" "}
          <strong className="text-emerald-200">Share</strong> →{" "}
          <strong className="text-emerald-200">Add to Home Screen</strong>.
        </span>
        <button
          type="button"
          onClick={() => {
            try {
              localStorage.setItem(DISMISS_KEY, "1");
            } catch {
              /* ignore */
            }
            setVisible(false);
          }}
          className="rounded-sm px-2 py-1 text-emerald-300 hover:bg-emerald-900/40 hover:text-emerald-100"
          aria-label="Dismiss"
        >
          ✕
        </button>
      </div>
    </div>
  );
}
