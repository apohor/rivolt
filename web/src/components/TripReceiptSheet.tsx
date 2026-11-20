import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { ApiError, backend, type SavedTrip, type TripCostEstimate } from "../lib/api";
import { usePreferences } from "../lib/preferences";
import { paintReceipt, receiptBlob } from "../lib/receiptCard";
import { buildReceiptModel, receiptText, RECEIPT_FILE, RECEIPT_SAYS, RECEIPT_SKETCH_SAYS } from "../lib/tripReceipt";

const ring = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-300";
const btn = `min-h-11 rounded-lg border border-neutral-700 px-4 text-sm text-neutral-100 hover:border-neutral-500 disabled:opacity-50 ${ring}`;
const solid = `min-h-11 flex-1 rounded-lg bg-emerald-600 px-4 text-sm font-semibold text-white hover:bg-emerald-500 disabled:opacity-50 ${ring}`;

const costOf = (t: SavedTrip): TripCostEstimate | null => {
  const plan = t.plan as { costs?: TripCostEstimate[] } | undefined;
  return plan?.costs?.[0] ?? null;
};

// Share this trip as a receipt: an image made on the device from the server's receipt (areas only, rounded near the ends), shared, saved or
// copied. It makes no link and uploads nothing. The route sketch and the cost are off on every open and never remembered.
export function TripReceiptSheet({ trip, vehicleModel, onClose }: { trip: SavedTrip; vehicleModel: string; onClose: () => void }) {
  const { distanceUnit } = usePreferences();
  const [sketch, setSketch] = useState(false);
  const [cost, setCost] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const canvas = useRef<HTMLCanvasElement | null>(null);
  const dialog = useRef<HTMLDivElement | null>(null);
  const closeRef = useRef<HTMLButtonElement | null>(null);

  // The base receipt, and once the sketch is asked for, the one with segments (the cut is drawn per request, so it is kept: the preview and every export match).
  const q = useQuery({
    queryKey: ["trip-receipt", trip.id, sketch, distanceUnit],
    queryFn: () => backend.tripReceipt(trip.id, { sketch, metric: distanceUnit === "km" }),
    staleTime: Infinity,
    gcTime: 60_000,
    retry: false,
  });
  const model = q.data ? buildReceiptModel(q.data, { model: vehicleModel, unit: distanceUnit, includeSketch: sketch, includeCost: cost, cost: costOf(trip) }) : null;
  const hasCost = !!costOf(trip);
  useEffect(() => {
    if (!model || !canvas.current) return;
    if (paintReceipt(canvas.current, model)) canvas.current.style.aspectRatio = `${canvas.current.width} / ${canvas.current.height}`;
  }, [model]);

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    closeRef.current?.focus();
    return () => opener?.focus?.();
  }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") return onClose();
      if (e.key !== "Tab" || !dialog.current) return;
      const f = Array.from(dialog.current.querySelectorAll<HTMLElement>("button:not([disabled]), input:not([disabled])"));
      if (f.length === 0) return;
      const at = document.activeElement;
      if (e.shiftKey && (at === f[0] || !dialog.current.contains(at))) {
        e.preventDefault();
        f[f.length - 1].focus();
      } else if (!e.shiftKey && (at === f[f.length - 1] || !dialog.current.contains(at))) {
        e.preventDefault();
        f[0].focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const error = q.error instanceof ApiError && q.error.status === 409 ? "A receipt isn't available for this trip yet (multi-day trips and trips with no route line)." : q.error instanceof ApiError && q.error.status === 422 ? "This trip is too short for a receipt." : q.isError ? "Couldn't make the receipt. Try again." : null;

  async function blob(): Promise<Blob> {
    if (!model) throw new Error("The receipt isn't ready yet.");
    const b = await receiptBlob(model);
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
  const ready = !!model && !busy;

  return createPortal(
    <div className="fixed inset-0 z-[1200] flex items-end justify-center bg-black/60 p-0 sm:items-center sm:p-4" onClick={onClose}>
      <div
        ref={dialog}
        role="dialog"
        aria-modal="true"
        aria-labelledby="trip-receipt-title"
        data-testid="trip-receipt-sheet"
        onClick={(e) => e.stopPropagation()}
        className="max-h-[92vh] w-full max-w-md space-y-4 overflow-y-auto rounded-t-xl border border-neutral-800 bg-neutral-950 p-4 sm:rounded-xl"
      >
        <div className="flex items-start justify-between gap-3">
          <h2 id="trip-receipt-title" className="text-[16px] font-semibold text-neutral-50">Share this trip</h2>
          <button ref={closeRef} type="button" onClick={onClose} aria-label="Close" className={`-mr-2 -mt-2 min-h-11 min-w-11 rounded-md text-neutral-400 hover:text-neutral-100 ${ring}`}>
            ✕
          </button>
        </div>
        {error ? (
          <p role="alert" className="text-sm text-amber-300" data-testid="trip-receipt-error">{error}</p>
        ) : (
          <canvas ref={canvas} role="img" aria-label={model ? `Preview of the trip receipt. ${receiptText(model).replace(/\n/g, ". ")}` : "Preview of the trip receipt"} data-testid="trip-receipt-canvas" className="w-full rounded-lg border border-neutral-800" style={{ aspectRatio: "1080 / 1500" }} />
        )}
        <label className="flex min-h-11 items-start gap-3 text-sm text-neutral-200">
          <input type="checkbox" className="mt-1 size-5 accent-emerald-500" checked={sketch} onChange={(e) => setSketch(e.target.checked)} data-testid="trip-receipt-sketch" />
          <span>
            Include route sketch
            <span className="block text-xs text-neutral-400">{RECEIPT_SKETCH_SAYS}</span>
          </span>
        </label>
        {hasCost && (
          <label className="flex min-h-11 items-start gap-3 text-sm text-neutral-200">
            <input type="checkbox" className="mt-1 size-5 accent-emerald-500" checked={cost} onChange={(e) => setCost(e.target.checked)} data-testid="trip-receipt-cost" />
            <span>
              Include charging cost
              <span className="block text-xs text-neutral-400">Adds the estimated charging cost of the trip.</span>
            </span>
          </label>
        )}
        <p className="text-xs text-neutral-400" data-testid="trip-receipt-says">{RECEIPT_SAYS} Made on your device. Nothing is uploaded or posted, and no link is made.</p>
        <div className="flex flex-wrap gap-2">
          {canShareFiles && (
            <button
              type="button"
              disabled={!ready}
              className={solid}
              onClick={() =>
                run(async () => {
                  const file = new File([await blob()], RECEIPT_FILE, { type: "image/png" });
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
            disabled={!ready}
            className={canShareFiles ? btn : solid}
            onClick={() =>
              run(async () => {
                const url = URL.createObjectURL(await blob());
                const a = document.createElement("a");
                a.href = url;
                a.download = RECEIPT_FILE;
                a.click();
                window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
                return `Saved ${RECEIPT_FILE}.`;
              })
            }
          >
            Save image
          </button>
          {canCopy && (
            <button
              type="button"
              disabled={!ready}
              className={btn}
              onClick={() =>
                run(async () => {
                  await navigator.clipboard.write([new ClipboardItem({ "image/png": blob() })]);
                  return "Copied.";
                })
              }
            >
              Copy
            </button>
          )}
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
