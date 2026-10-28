import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { paintShareCard, shareCardBlob, type ShareCardModel } from "../lib/shareCard";
import { ensureConfig } from "../lib/config";

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50";

// The share sheet: a preview of the card, the cost toggle (off every time it opens, never remembered), and
// Share / Save image / Copy image. The card is painted here, in the browser; nothing is uploaded or posted.
export function ShareSheet({
  build,
  fileName,
  withCostToggle,
  withOffpeakToggle = false,
  onClose,
}: {
  build: (includeCost: boolean, includeOffpeak: boolean) => ShareCardModel;
  fileName: string;
  withCostToggle: boolean;
  // The off-peak share line has its own toggle, shown only when there is a share to show.
  withOffpeakToggle?: boolean;
  onClose: () => void;
}) {
  const [includeCost, setIncludeCost] = useState(false);
  const [includeOffpeak, setIncludeOffpeak] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const canvas = useRef<HTMLCanvasElement | null>(null);
  const closeRef = useRef<HTMLButtonElement | null>(null);

  // The footer names the app once the app is listed; the config is awaited so a card opened early is drawn again with it.
  const [cfgReady, setCfgReady] = useState(false);
  useEffect(() => {
    let cancelled = false;
    void ensureConfig().then(() => {
      if (!cancelled) setCfgReady(true);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  useEffect(() => {
    if (canvas.current) paintShareCard(canvas.current, build(includeCost, includeOffpeak));
  }, [build, includeCost, includeOffpeak, cfgReady]);
  const dialog = useRef<HTMLDivElement | null>(null);
  // Focus goes into the sheet, Tab stays inside it, and closing hands focus back to what opened it.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    closeRef.current?.focus();
    return () => opener?.focus?.();
  }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") return onClose();
      if (e.key !== "Tab" || !dialog.current) return;
      const f = Array.from(dialog.current.querySelectorAll<HTMLElement>("button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex='-1'])"));
      if (f.length === 0) return;
      const first = f[0];
      const last = f[f.length - 1];
      const at = document.activeElement;
      if (e.shiftKey && (at === first || !dialog.current.contains(at))) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (at === last || !dialog.current.contains(at))) {
        e.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  async function blob(): Promise<Blob> {
    const b = await shareCardBlob(build(includeCost, includeOffpeak));
    if (!b) throw new Error("Image export isn't available in this browser.");
    return b;
  }
  const run = async (fn: () => Promise<string | null>) => {
    setBusy(true);
    setMsg(null);
    try {
      const done = await fn();
      if (done) setMsg({ tone: "ok", text: done });
    } catch (e) {
      if (!(e instanceof DOMException && e.name === "AbortError")) setMsg({ tone: "err", text: e instanceof Error ? e.message : "Couldn't create the image." });
    } finally {
      setBusy(false);
    }
  };
  const canShareFiles = typeof navigator !== "undefined" && typeof navigator.canShare === "function" && typeof navigator.share === "function";
  const canCopy = typeof window !== "undefined" && typeof ClipboardItem !== "undefined" && !!navigator.clipboard?.write;

  // Rendered into the body: inside a page header (or any element with its own stacking context) the bottom nav would
  // sit on top of the sheet and cover Cancel.
  return createPortal(
    <div className="fixed inset-0 z-[1200] flex items-end justify-center bg-black/60 p-0 sm:items-center sm:p-4" onClick={onClose}>
      <div
        ref={dialog}
        role="dialog"
        aria-modal="true"
        aria-labelledby="share-sheet-title"
        data-share-sheet
        onClick={(e) => e.stopPropagation()}
        className="max-h-[92vh] w-full max-w-md space-y-4 overflow-y-auto rounded-t-xl border border-neutral-800 bg-neutral-950 p-4 sm:rounded-xl"
      >
        <h2 id="share-sheet-title" className="text-base font-semibold text-neutral-100">
          Share a card
        </h2>
        <canvas ref={canvas} role="img" aria-label="Preview of the card" className="aspect-square w-full rounded-lg border border-neutral-800" />
        {withCostToggle && (
          <label className="flex items-start gap-2 text-sm text-neutral-200">
            <input type="checkbox" className="mt-1 accent-emerald-500" checked={includeCost} onChange={(e) => setIncludeCost(e.target.checked)} />
            <span>
              Include cost
              <span className="block text-xs text-neutral-400">Adds charging cost and cost per mile.</span>
            </span>
          </label>
        )}
        {withOffpeakToggle && (
          <label className="flex items-start gap-2 text-sm text-neutral-200">
            <input type="checkbox" className="mt-1 accent-emerald-500" checked={includeOffpeak} onChange={(e) => setIncludeOffpeak(e.target.checked)} />
            <span>
              Include off-peak share
              <span className="block text-xs text-neutral-400">Adds the percentage of home charging that ran in your off-peak hours. No rates, no dollars, no times.</span>
            </span>
          </label>
        )}
        <p className="text-xs text-neutral-500">Made on your device. Nothing is uploaded or posted.</p>
        <div className="flex flex-wrap gap-2">
          {canShareFiles && (
            <button
              type="button"
              disabled={busy}
              className={btn}
              onClick={() =>
                run(async () => {
                  const file = new File([await blob()], fileName, { type: "image/png" });
                  if (!navigator.canShare({ files: [file] })) throw new Error("This browser can't share an image. Use Save image.");
                  await navigator.share({ files: [file], title: "Rivolt" });
                  return "Shared.";
                })
              }
            >
              Share
            </button>
          )}
          <button
            type="button"
            disabled={busy}
            className={btn}
            onClick={() =>
              run(async () => {
                const url = URL.createObjectURL(await blob());
                const a = document.createElement("a");
                a.href = url;
                a.download = fileName;
                a.click();
                // Revoking at once can cancel the download in Safari and Firefox.
                window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
                return `Saved ${fileName}.`;
              })
            }
          >
            Save image
          </button>
          {canCopy && (
            <button
              type="button"
              disabled={busy}
              className={btn}
              onClick={() =>
                run(async () => {
                  await navigator.clipboard.write([new ClipboardItem({ "image/png": blob() })]);
                  return "Copied.";
                })
              }
            >
              Copy image
            </button>
          )}
          <button ref={closeRef} type="button" className={btn} onClick={onClose}>
            Cancel
          </button>
        </div>
        {msg && (
          <p role={msg.tone === "err" ? "alert" : "status"} className={`text-xs ${msg.tone === "err" ? "text-rose-300" : "text-emerald-300"}`}>
            {msg.text}
          </p>
        )}
      </div>
    </div>,
    document.body,
  );
}
