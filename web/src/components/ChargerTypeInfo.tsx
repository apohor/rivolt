import { useEffect, useId, useRef, useState } from "react";
import { chargerLabel, chargerSentence, INFO_LABEL, type ChargerKind } from "../lib/chargerLabels";

// The (i) beside charger-type words: a tap or click opens one plain sentence per type (never hover-only). Escape or a tap elsewhere closes it, and
// focus goes back to the button. `floating` anchors the popover to the nearest positioned ancestor (a row of chips) instead of to the button.
export function ChargerTypeInfo({ kinds, floating = false, className = "" }: { kinds: ChargerKind[]; floating?: boolean; className?: string }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const root = useRef<HTMLSpanElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        button.current?.focus();
      }
    };
    document.addEventListener("pointerdown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return (
    <span ref={root} data-charger-info className={`${floating ? "" : "relative "}inline-flex align-middle ${className}`}>
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
      {open && (
        <div
          id={id}
          role="group"
          aria-label={INFO_LABEL}
          className="pointer-events-auto absolute left-0 top-full z-[700] mt-1 w-[min(20rem,calc(100vw-2rem))] space-y-2 rounded-lg border border-neutral-700 bg-neutral-900 p-3 text-left text-xs font-normal normal-case leading-snug tracking-normal text-neutral-300 shadow-lg"
        >
          {kinds.map((k) => (
            <p key={k}>
              <b className="block text-neutral-100">{chargerLabel(k)}</b>
              {chargerSentence(k)}
            </p>
          ))}
        </div>
      )}
    </span>
  );
}
