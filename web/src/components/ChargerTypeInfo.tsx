import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { chargerLabel, chargerSentence, INFO_LABEL, type ChargerKind } from "../lib/chargerLabels";

// The (i) beside charger-type words: a tap or click opens one plain sentence per type (never hover-only). Escape or a tap anywhere else closes it, and
// focus goes back to the button on Escape.
//
// The popover is drawn in the page body, positioned from the button's rectangle and kept inside the window, not inside the (clipped, blurred) row it
// came from: inside a map's overflow-hidden box a long list was cut off at the map's edge. When the list is taller than the room it scrolls.
const MARGIN = 8;
const WIDTH = 320;

export function ChargerTypeInfo({ kinds, className = "" }: { kinds: ChargerKind[]; className?: string }) {
  const [open, setOpen] = useState(false);
  const [box, setBox] = useState<{ left: number; top?: number; bottom?: number; width: number; maxHeight: number } | null>(null);
  const id = useId();
  const root = useRef<HTMLSpanElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const pop = useRef<HTMLDivElement>(null);

  const place = useCallback(() => {
    const b = button.current?.getBoundingClientRect();
    if (!b) return;
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const width = Math.min(WIDTH, vw - 2 * MARGIN);
    const left = Math.max(MARGIN, Math.min(b.left, vw - width - MARGIN));
    const below = vh - b.bottom - MARGIN * 2;
    const above = b.top - MARGIN * 2;
    // Below the button unless there is clearly more room above it.
    if (below >= 220 || below >= above) setBox({ left, top: b.bottom + MARGIN / 2, width, maxHeight: Math.max(120, below) });
    else setBox({ left, bottom: vh - b.top + MARGIN / 2, width, maxHeight: Math.max(120, above) });
  }, []);

  useLayoutEffect(() => {
    if (open) place();
  }, [open, place]);

  useEffect(() => {
    if (!open) return;
    // Capture phase on pointer, touch and mouse events: a map (or anything else) that handles or swallows its own gesture still closes the popover.
    const outside = (e: Event) => {
      const t = e.target as Node | null;
      if (t && (root.current?.contains(t) || pop.current?.contains(t))) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        button.current?.focus();
      }
    };
    const events = ["pointerdown", "touchstart", "mousedown"] as const;
    for (const ev of events) document.addEventListener(ev, outside, true);
    document.addEventListener("keydown", onKey);
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      for (const ev of events) document.removeEventListener(ev, outside, true);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open, place]);

  return (
    <span ref={root} data-charger-info className={`inline-flex align-middle ${className}`}>
      <button
        ref={button}
        type="button"
        aria-label={INFO_LABEL}
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => setOpen((v) => !v)}
        className="pointer-events-auto inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-neutral-600 bg-neutral-900 font-serif text-xs italic leading-none text-neutral-300 hover:text-neutral-100"
      >
        i
      </button>
      {open &&
        box &&
        createPortal(
          <div
            ref={pop}
            id={id}
            role="group"
            aria-label={INFO_LABEL}
            data-charger-info-popover
            style={{ position: "fixed", left: box.left, top: box.top, bottom: box.bottom, width: box.width, maxHeight: box.maxHeight }}
            className="z-[1200] space-y-2 overflow-y-auto rounded-lg border border-neutral-700 bg-neutral-900 p-3 text-left text-xs font-normal normal-case leading-snug tracking-normal text-neutral-300 shadow-lg"
          >
            {kinds.map((k) => (
              <p key={k}>
                <b className="block text-neutral-100">{chargerLabel(k)}</b>
                {chargerSentence(k)}
              </p>
            ))}
          </div>,
          document.body,
        )}
    </span>
  );
}
