import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent, type MouseEvent, type PointerEvent, type ReactNode } from "react";

// A bottom sheet for phones (ui_theme_next planner): the summary peeks over
// the map; drag the handle, or use the keyboard, to raise it to half height
// or full. At 1024px and wider it is not a sheet: the content renders inline
// and the page's grid lays it out.
//
// Accessibility: the sheet is a region; the handle is a real button with
// aria-expanded and arrow-key control, so drag is never the only way; the
// part that is off-screen when the sheet is at its peek is inert, so Tab and
// screen-reader virtual cursors cannot land on something hidden. It is not
// modal: no focus trap, the map behind stays usable.
export type Snap = "peek" | "half" | "full";

const ORDER: readonly Snap[] = ["peek", "half", "full"];

export interface SnapHeights {
  peek: number;
  half: number;
  full: number;
}

// snapHeights are the three sheet heights in px for a viewport height. Half is
// half the viewport; full leaves a strip of the page above the sheet; peek is
// fixed. They are kept strictly increasing, even in a very short window.
export function snapHeights(viewportH: number, peekPx: number): SnapHeights {
  const full = Math.max(Math.round(viewportH * 0.88), peekPx + 80);
  const half = Math.min(Math.max(Math.round(viewportH * 0.5), peekPx + 40), full - 40);
  return { peek: peekPx, half, full };
}

// nearestSnap picks the snap a released drag settles on. velocity is px/ms,
// positive when the sheet was moving up. A flick (at least 0.5 px/ms) steps
// one snap from where the drag began (from) in its direction; a slower
// release settles on the snap nearest the release height.
export function nearestSnap(height: number, h: SnapHeights, velocity: number, from?: Snap): Snap {
  const nearest = (): Snap => {
    let best: Snap = "peek";
    let d = Infinity;
    for (const s of ORDER) {
      const dist = Math.abs(h[s] - height);
      if (dist < d) {
        d = dist;
        best = s;
      }
    }
    return best;
  };
  if (Math.abs(velocity) >= 0.5) return stepSnap(from ?? nearest(), velocity > 0 ? 1 : -1);
  return nearest();
}

// stepSnap moves one snap up (dir 1) or down (dir -1), stopping at the ends.
export function stepSnap(s: Snap, dir: 1 | -1): Snap {
  const i = ORDER.indexOf(s) + dir;
  return ORDER[Math.min(ORDER.length - 1, Math.max(0, i))];
}

function useMediaQuery(query: string): boolean {
  const get = () => typeof window !== "undefined" && !!window.matchMedia && window.matchMedia(query).matches;
  const [match, setMatch] = useState(get);
  useEffect(() => {
    if (!window.matchMedia) return;
    const mq = window.matchMedia(query);
    const on = () => setMatch(mq.matches);
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [query]);
  return match;
}

function useViewportHeight(): number {
  const get = () => (typeof window === "undefined" ? 800 : window.innerHeight);
  const [h, setH] = useState(get);
  useEffect(() => {
    const on = () => setH(get());
    window.addEventListener("resize", on);
    return () => window.removeEventListener("resize", on);
  }, []);
  return h;
}

export const SHEET_DESKTOP_QUERY = "(min-width: 1024px)";

export function BottomSheet({
  label,
  peek,
  children,
  snap: controlled,
  defaultSnap = "peek",
  onSnapChange,
  peekPx = 120,
  className,
}: {
  // Accessible name of the region, e.g. "Trip details".
  label: string;
  // Always visible, always interactive: what shows when the sheet is at its peek.
  peek?: ReactNode;
  // The rest; inert while the sheet is at its peek.
  children?: ReactNode;
  snap?: Snap;
  defaultSnap?: Snap;
  onSnapChange?: (s: Snap) => void;
  peekPx?: number;
  className?: string;
}) {
  const desktop = useMediaQuery(SHEET_DESKTOP_QUERY);
  const vh = useViewportHeight();
  const heights = snapHeights(vh, peekPx);
  const [own, setOwn] = useState<Snap>(defaultSnap);
  const snap = controlled ?? own;
  const set = useCallback(
    (s: Snap) => {
      if (controlled === undefined) setOwn(s);
      onSnapChange?.(s);
    },
    [controlled, onSnapChange],
  );
  const contentId = useId();

  // Drag state: the live height while a pointer is down (null otherwise).
  const [dragH, setDragH] = useState<number | null>(null);
  const drag = useRef<{ y: number; h: number; from: Snap; lastY: number; lastT: number; v: number } | null>(null);

  if (desktop) {
    // Not a sheet on a wide screen: the page's grid places these inline.
    return (
      <div data-plan-sheet-inline className={className}>
        {peek}
        {children}
      </div>
    );
  }

  const onDown = (e: PointerEvent<HTMLButtonElement>) => {
    if (e.button !== 0 && e.pointerType === "mouse") return;
    e.currentTarget.setPointerCapture?.(e.pointerId);
    drag.current = { y: e.clientY, h: heights[snap], from: snap, lastY: e.clientY, lastT: e.timeStamp, v: 0 };
  };
  const onMove = (e: PointerEvent<HTMLButtonElement>) => {
    const d = drag.current;
    if (!d) return;
    const dy = d.y - e.clientY; // up is positive
    if (dragH === null && Math.abs(dy) < 4) return; // a tap, not a drag
    const dt = e.timeStamp - d.lastT;
    if (dt > 0) d.v = (d.lastY - e.clientY) / dt;
    d.lastY = e.clientY;
    d.lastT = e.timeStamp;
    setDragH(Math.min(heights.full, Math.max(heights.peek, d.h + dy)));
  };
  const onUp = () => {
    const d = drag.current;
    drag.current = null;
    if (!d) return;
    if (dragH !== null) {
      set(nearestSnap(dragH, heights, d.v, d.from));
      setDragH(null);
    }
    // No drag: the click handler toggles.
  };
  const onClick = () => {
    if (dragH !== null) return;
    set(snap === "peek" ? "half" : "peek");
  };
  const onKey = (e: KeyboardEvent<HTMLElement>) => {
    switch (e.key) {
      case "ArrowUp":
        e.preventDefault();
        set(stepSnap(snap, 1));
        break;
      case "ArrowDown":
        e.preventDefault();
        set(stepSnap(snap, -1));
        break;
      case "Home":
        e.preventDefault();
        set("peek");
        break;
      case "End":
        e.preventDefault();
        set("full");
        break;
    }
  };
  // Tapping the peek (the summary) raises the sheet, like tapping the handle,
  // unless the tap was on something interactive inside it (the route chooser,
  // a link): those keep their own action. Pointer convenience only; the handle
  // is the keyboard and screen-reader way.
  const onPeekClick = (e: MouseEvent<HTMLDivElement>) => {
    if (snap !== "peek") return;
    const t = e.target as Element | null;
    if (t?.closest("button, a, input, select, textarea, label, summary, [role='button'], [role='tab'], [role='switch'], [role='radio'], [role='slider']")) return;
    set("half");
  };
  const onRegionKey = (e: KeyboardEvent<HTMLElement>) => {
    // Escape from anywhere in the sheet folds it back to the peek.
    if (e.key === "Escape" && snap !== "peek") {
      e.stopPropagation();
      set("peek");
    }
  };

  const height = dragH ?? heights[snap];
  const expanded = snap !== "peek";
  return (
    <section
      aria-label={label}
      data-plan-sheet
      data-snap={snap}
      data-dragging={dragH !== null ? "true" : undefined}
      className={className}
      style={{ height, ["--sheet-h" as string]: `${height}px` }}
      onKeyDown={onRegionKey}
    >
      <button
        type="button"
        data-plan-sheet-handle
        aria-expanded={expanded}
        aria-controls={contentId}
        aria-label={expanded ? `Collapse ${label}` : `Expand ${label}`}
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerCancel={onUp}
        onClick={onClick}
        onKeyDown={onKey}
      />
      {peek != null && (
        // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions
        <div data-plan-sheet-peek onClick={onPeekClick}>
          {peek}
        </div>
      )}
      <div id={contentId} data-plan-sheet-body inert={!expanded}>
        {children}
      </div>
    </section>
  );
}
