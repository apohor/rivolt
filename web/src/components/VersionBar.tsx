import { useCallback, useEffect, useRef, useState } from "react";
import { backend } from "../lib/api";
import { VERSION_POLL_MS, baselineVersion, isPreviewHost, nextAction } from "../lib/versionCheck";

// "New version available" bar. Polls /api/health every minute while the page is
// visible, and on visibilitychange/focus. Prod: the bar only, never an
// auto-reload. Preview: reloads on its own once the tab is hidden, so the next
// time the owner switches back they are on the new build. Offline or failing
// requests are ignored (the offline shell keeps working).
export function VersionBar() {
  const [fresh, setFresh] = useState(false);
  const firstSeen = useRef<string | null>(null);
  const preview = isPreviewHost(window.location.hostname);

  const reload = useCallback(async () => {
    try {
      const reg = await navigator.serviceWorker?.getRegistration();
      await Promise.race([reg?.update(), new Promise((r) => setTimeout(r, 3000))]);
    } catch {
      /* a failed SW update must not block the reload */
    }
    window.location.reload();
  }, []);

  useEffect(() => {
    let stopped = false;
    const check = async () => {
      if (stopped || navigator.onLine === false) return;
      let live: string | null;
      try {
        live = (await backend.health()).version ?? null;
      } catch {
        return;
      }
      if (stopped) return;
      if (firstSeen.current == null) firstSeen.current = live;
      const action = nextAction({
        baseline: baselineVersion(firstSeen.current),
        live,
        hidden: document.visibilityState === "hidden",
        preview,
      });
      if (action === "reload") void reload();
      else if (action === "prompt") setFresh(true);
    };
    void check();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void check();
    }, VERSION_POLL_MS);
    // Becoming visible or focused checks right away; becoming hidden checks too, which
    // is when preview reloads itself.
    const onVis = () => void check();
    document.addEventListener("visibilitychange", onVis);
    window.addEventListener("focus", onVis);
    return () => {
      stopped = true;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVis);
      window.removeEventListener("focus", onVis);
    };
  }, [preview, reload]);

  if (!fresh) return null;
  return (
    <div
      role="status"
      data-testid="version-bar"
      className="fixed inset-x-0 z-50 mx-auto flex w-fit max-w-[calc(100%-2rem)] items-center gap-3 rounded-full border border-emerald-600/40 bg-neutral-900 px-4 py-2 text-sm text-neutral-100 shadow-lg"
      style={{ bottom: "calc(env(safe-area-inset-bottom, 0px) + 5rem)" }}
    >
      <span>New version available</span>
      <button
        type="button"
        onClick={() => void reload()}
        className="rounded-full bg-emerald-600 px-3 py-1 text-xs font-semibold text-white hover:bg-emerald-500"
      >
        Reload
      </button>
    </div>
  );
}
