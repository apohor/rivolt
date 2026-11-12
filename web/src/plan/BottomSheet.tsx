import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type MouseEvent, type PointerEvent, type ReactNode } from "react";

// A bottom sheet for phones (planner), with the iPhone app's three heights: the peek (a title and one summary line over the map), half, and full
// (the details). A tap on the peek opens the details and the Map button shows the map again; an upright drag anywhere on the sheet moves it until it
// is fully open, and there the details scroll (a drag down with them at their top closes it again). A sideways drag does nothing, and the buttons in
// the sheet keep their taps. At 1024px and wider it is not a sheet: the content renders inline and the page's grid lays it out.
//
// Accessibility: the sheet is a region; the handle is a real button whose name says how open the sheet is, with arrow-key control, so drag is never
// the only way; at full the focus moves into the details, and back to the handle at the peek. The part that is off-screen at the peek is inert, so
// Tab and screen-reader virtual cursors cannot land on something hidden. It is not modal: no focus trap, the map behind stays usable.
//
// On a very short screen (Safari's aA 300 %: 131 x 232 CSS px) the peek leaves the map a strip some 60 px tall, so there Map shows at the peek too and
// lowers the sheet to its handle alone ("min"); the handle, a drag up or the up arrow raise it again. Elsewhere min is never used.
export type Snap = "min" | "peek" | "half" | "full";

// The heights a drag moves between (min is reached by Map only).
type Detent = Exclude<Snap, "min">;
const ORDER: readonly Detent[] = ["peek", "half", "full"];

export interface SnapHeights {
  peek: number;
  half: number;
  full: number;
}

// The peek may not take more than this share of the room, whatever its text needs; on a short room (a phone at Safari's aA 200 %) it may
// still take up to PEEK_FLOOR px, never more than half the room, so a wrapped title and summary are not cut off.
export const PEEK_CAP = 0.35;
export const PEEK_FLOOR = 150;
// A drag moves the sheet one step further than the last height it passed once it has gone this share of the way to the next one, or when it is
// let go at FLICK_SPEED (px/ms) or faster.
export const STEP_SHARE = 1 / 3;
export const FLICK_SPEED = 0.5;
// How far a finger goes before the sheet decides between a drag (upright) and leaving it alone (sideways), and a tap stays a tap.
export const DRAG_SLOP = 10;

// snapHeights are the three sheet heights in px for the room the sheet has (from above the tab bar to under the top bar, measured, not the
// viewport): the peek is what its title and summary line need (peekNeed) up to 35 % of the room, half is about half, full is all of it, or all of
// fullRoom when that is taller (on a very short screen the open sheet also covers the top bar). They are kept strictly increasing, even in a very
// short window.
export function snapHeights(room: number, peekNeed: number, fullRoom = room): SnapHeights {
  const cap = Math.max(room * PEEK_CAP, Math.min(PEEK_FLOOR, room * 0.5));
  const peek = Math.round(peekNeed > 0 ? Math.min(peekNeed, cap) : Math.min(150, room * 0.3));
  const full = Math.max(Math.round(room), Math.round(fullRoom), peek + 80);
  const half = Math.min(Math.max(Math.round(room * 0.46), peek + 40), full - 40);
  return { peek, half, full };
}

// settleSnap picks the snap a released drag settles on. raised is how far the sheet was moved (px, up is positive) from the snap the drag began at,
// velocity its speed at release (px/ms, up is positive). The sheet goes to the last height the drag passed, and one step further when it went a third
// of the way to the next or was flicked that way: a medium flick from the peek opens half, not the details.
export function settleSnap(from: Detent, raised: number, velocity: number, h: SnapHeights): Snap {
  const flick = Math.abs(velocity) >= FLICK_SPEED;
  const up = flick ? velocity > 0 : raised >= 0;
  if (!flick && Math.abs(raised) <= 0.5) return from;
  const heights = ORDER.map((s) => h[s]);
  let at = ORDER.indexOf(from);
  const pos = heights[at] + raised;
  const step = up ? 1 : -1;
  // The heights the drag passed, in the direction it was going.
  while (at + step >= 0 && at + step < ORDER.length && (up ? pos >= heights[at + step] : pos <= heights[at + step])) at += step;
  if (at + step < 0 || at + step >= ORDER.length) return ORDER[at];
  const gap = Math.abs(heights[at + step] - heights[at]);
  const past = up ? pos - heights[at] : heights[at] - pos;
  const flickedThatWay = flick && velocity > 0 === up;
  return past >= gap * STEP_SHARE || flickedThatWay ? ORDER[at + step] : ORDER[at];
}

// dragMovesSheet: below full an upright drag always moves the sheet; at full, where the details scroll, only a drag down with them at their top.
export function dragMovesSheet(snap: Snap, inBody: boolean, bodyAtTop: boolean, raised: number): boolean {
  return snap !== "full" || !inBody || (bodyAtTop && raised < 0);
}

// releaseVelocity is a drag's speed when it is let go (px/ms, up is positive), from its moves in the last VELOCITY_WINDOW ms (single samples are
// noisy when the browser coalesces moves). A finger that held still before lifting has no speed: a drag, a hold and a release is not a flick.
export const VELOCITY_WINDOW = 80;
export const HOLD_MS = 100;
export function releaseVelocity(samples: readonly { y: number; t: number }[], releaseT: number): number {
  const last = samples[samples.length - 1];
  if (!last || releaseT - last.t > HOLD_MS) return 0;
  const first = samples.find((p) => p.t >= last.t - VELOCITY_WINDOW) ?? last;
  const dt = last.t - first.t;
  return dt > 0 ? (first.y - last.y) / dt : 0;
}

// stepSnap moves one snap up (dir 1) or down (dir -1), stopping at the ends: the peek, or min when the sheet can be lowered to it.
export function stepSnap(s: Snap, dir: 1 | -1, lowerable = false): Snap {
  if (s === "min") return dir === 1 ? "peek" : "min";
  if (s === "peek" && dir === -1 && lowerable) return "min";
  const i = ORDER.indexOf(s) + dir;
  return ORDER[Math.min(ORDER.length - 1, Math.max(0, i))];
}

// The handle's name says how open the sheet is and what activating it does.
export function handleLabel(label: string, snap: Snap): string {
  return snap === "min" ? `${label}: lowered, expand` : snap === "peek" ? `${label}: collapsed, expand` : snap === "half" ? `${label}: half open, expand fully` : `${label}: fully open, collapse`;
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

// useHeight follows an element's height (0 until it is laid out, and in jsdom).
function useHeight(el: HTMLElement | null, extra?: () => number): number {
  const [h, setH] = useState(0);
  useLayoutEffect(() => {
    if (!el) return;
    const on = () => setH(el.offsetHeight + (extra?.() ?? 0));
    on();
    const ro = typeof ResizeObserver !== "undefined" ? new ResizeObserver(on) : null;
    ro?.observe(el);
    window.addEventListener("resize", on);
    return () => {
      ro?.disconnect();
      window.removeEventListener("resize", on);
    };
  }, [el, extra]);
  return h;
}

export const SHEET_DESKTOP_QUERY = "(min-width: 1024px)";
// The very short screen where the sheet can be lowered to its handle (the same height as the sheet's short-screen CSS).
export const SHEET_LOWERABLE_QUERY = "(max-height: 299.98px)";

const INTERACTIVE = "button, a, input, select, textarea, label, summary, [role='button'], [role='tab'], [role='switch'], [role='radio'], [role='slider']";

// A drag that begins in one of these scrolls it sideways when it goes sideways: the sheet does not claim its first moves.
const PAN_X = "[data-sheet-pan-x]";

type Drag = { x: number; y: number; h: number; from: Snap; inBody: boolean; bodyAtTop: boolean; panX: boolean; samples: { y: number; t: number }[]; mode: "pending" | "sheet" | "off" };

export function BottomSheet({
  label,
  peek,
  children,
  snap: controlled,
  defaultSnap = "peek",
  onSnapChange,
  className,
}: {
  // Accessible name of the region, e.g. "Trip details".
  label: string;
  // The sheet's title and one summary line, always visible and interactive: what shows at the peek. Not rendered at 1024px and up.
  peek?: ReactNode;
  // The rest; inert while the sheet is at its peek. Rendered inline at 1024px and up.
  children?: ReactNode;
  snap?: Snap;
  defaultSnap?: Snap;
  onSnapChange?: (s: Snap) => void;
  className?: string;
}) {
  const desktop = useMediaQuery(SHEET_DESKTOP_QUERY);
  const lowerable = useMediaQuery(SHEET_LOWERABLE_QUERY);
  const [own, setOwn] = useState<Snap>(defaultSnap);
  const asked = controlled ?? own;
  // A window that grows out of the short screen (rotated) brings a lowered sheet back to its peek.
  const snap = asked === "min" && !lowerable ? "peek" : asked;
  // A change made from inside the sheet moves the focus with it (into the details at full, to the handle at the peek); one the page makes does not.
  const focusNext = useRef(false);
  const set = useCallback(
    (s: Snap, focus = true) => {
      focusNext.current = focus;
      if (controlled === undefined) setOwn(s);
      onSnapChange?.(s);
    },
    [controlled, onSnapChange],
  );
  const contentId = useId();

  // The room is measured from a fixed box with the sheet's own limits (CSS), so the browser's bars and the safe areas are already out of it.
  const [roomEl, setRoomEl] = useState<HTMLDivElement | null>(null);
  const [fullRoomEl, setFullRoomEl] = useState<HTMLDivElement | null>(null);
  const [peekEl, setPeekEl] = useState<HTMLDivElement | null>(null);
  const [handleEl, setHandleEl] = useState<HTMLButtonElement | null>(null);
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const measuredRoom = useHeight(roomEl);
  const measuredFullRoom = useHeight(fullRoomEl);
  const handleH = useCallback(() => handleEl?.offsetHeight ?? 0, [handleEl]);
  const peekNeed = useHeight(peekEl, handleH);
  const room = measuredRoom > 0 ? measuredRoom : Math.round((typeof window === "undefined" ? 800 : window.innerHeight) * 0.88);
  const heights = snapHeights(room, peekNeed, measuredFullRoom);
  // Lowered: the handle alone.
  const minH = Math.min(heights.peek, handleEl?.offsetHeight || 32);

  // Drag state: the live height while a drag moves the sheet (null otherwise).
  const [dragH, setDragH] = useState<number | null>(null);
  const drag = useRef<Drag | null>(null);
  const dragged = useRef(false);

  // The moves read the latest heights and snap through refs: the touch listeners are attached once.
  const live = useRef({ heights, minH, snap, set });
  live.current = { heights, minH, snap, set };

  const begin = (x: number, y: number, t: number, target: EventTarget | null) => {
    const body = bodyRef.current;
    const inBody = !!body && target instanceof Node && body.contains(target);
    dragged.current = false;
    const panX = target instanceof Element && !!target.closest(PAN_X);
    const from = live.current.snap;
    drag.current = { x, y, h: from === "min" ? live.current.minH : live.current.heights[from], from, inBody, bodyAtTop: !body || body.scrollTop <= 0, panX, samples: [{ y, t }], mode: "pending" };
  };
  // Returns whether the sheet took this move (the caller then keeps the page and the details from scrolling).
  const moveTo = (x: number, y: number, t: number): boolean => {
    const d = drag.current;
    if (!d || d.mode === "off") return false;
    const raised = d.y - y;
    if (d.mode === "pending") {
      const dx = x - d.x;
      // Within the slop, keep the details from starting a scroll the sheet may yet take (a drag down with them at their top), but not in a sideways
      // scroller: cancelling its first move would cost it the whole swipe.
      if (Math.hypot(dx, raised) < DRAG_SLOP) return !d.panX && dragMovesSheet(d.from, d.inBody, d.bodyAtTop, raised);
      d.mode = Math.abs(raised) > Math.abs(dx) && dragMovesSheet(d.from, d.inBody, d.bodyAtTop, raised) ? "sheet" : "off";
      if (d.mode === "off") return false;
    }
    d.samples.push({ y, t });
    if (d.samples.length > 12) d.samples.shift();
    const h = live.current.heights;
    setDragH(Math.min(h.full, Math.max(d.from === "min" ? live.current.minH : h.peek, d.h + raised)));
    return true;
  };
  // Ends a drag at y (null: where it last was) at time t (null: no speed, e.g. a second finger landed).
  const finish = (y: number | null, t: number | null) => {
    const d = drag.current;
    drag.current = null;
    setDragH(null);
    if (!d || d.mode === "pending") return;
    // A move past the slop, either way, is not a tap: the click a mouse ends it with is dropped.
    dragged.current = true;
    if (d.mode !== "sheet") return;
    const last = d.samples[d.samples.length - 1];
    const raised = d.y - (y ?? last.y);
    const velocity = t === null ? 0 : releaseVelocity(d.samples, t);
    const { heights: h, minH: lowH } = live.current;
    // From min, a drag up goes on from the peek (at least back to it); a drag down leaves it lowered.
    if (d.from === "min") live.current.set(raised > 0 ? settleSnap("peek", Math.max(0, raised - (h.peek - lowH)), velocity, h) : "min");
    else live.current.set(settleSnap(d.from, raised, velocity, h));
  };

  // Touch: the browser's own listeners are passive, so these are added by hand (a move the sheet takes must not also scroll the page).
  const begunRef = useRef(begin);
  const moveRef = useRef(moveTo);
  const finishRef = useRef(finish);
  begunRef.current = begin;
  moveRef.current = moveTo;
  finishRef.current = finish;
  const [sectionEl, setSectionEl] = useState<HTMLElement | null>(null);
  useEffect(() => {
    const el = sectionEl;
    if (!el) return;
    const start = (e: TouchEvent) => {
      // A second finger ends the drag where it is (settled, never stranded between heights); a pinch is the browser's.
      if (e.touches.length !== 1) return finishRef.current(null, null);
      const p = e.touches[0];
      begunRef.current(p.clientX, p.clientY, e.timeStamp, e.target);
    };
    const move = (e: TouchEvent) => {
      const p = e.touches[0];
      if (p && moveRef.current(p.clientX, p.clientY, e.timeStamp) && e.cancelable) e.preventDefault();
    };
    const end = (e: TouchEvent) => {
      // Lifting one of two fingers is not the end of a drag (there is none by then).
      if (e.touches.length > 0) return;
      finishRef.current(e.changedTouches[0]?.clientY ?? null, e.timeStamp);
    };
    el.addEventListener("touchstart", start, { passive: true });
    el.addEventListener("touchmove", move, { passive: false });
    el.addEventListener("touchend", end);
    const cancel = () => finishRef.current(null, null);
    el.addEventListener("touchcancel", cancel);
    return () => {
      el.removeEventListener("touchstart", start);
      el.removeEventListener("touchmove", move);
      el.removeEventListener("touchend", end);
      el.removeEventListener("touchcancel", cancel);
    };
  }, [sectionEl]);

  // The focus follows a change made from inside the sheet.
  const lastSnap = useRef(snap);
  useEffect(() => {
    if (lastSnap.current === snap) return;
    lastSnap.current = snap;
    if (!focusNext.current) return;
    focusNext.current = false;
    if (snap === "full") bodyRef.current?.focus({ preventScroll: true });
    else if (snap === "peek" || snap === "min") handleEl?.focus({ preventScroll: true });
  }, [snap, handleEl]);

  if (desktop) {
    // Not a sheet on a wide screen: the page's grid places the content inline.
    return (
      <div data-plan-sheet-inline className={className}>
        {children}
      </div>
    );
  }

  // Mouse and pen; touch has its own listeners above (the two would both fire for one finger).
  const onDown = (e: PointerEvent<HTMLElement>) => {
    if (e.pointerType === "touch") return;
    if (e.button !== 0) return;
    begin(e.clientX, e.clientY, e.timeStamp, e.target);
  };
  const onMove = (e: PointerEvent<HTMLElement>) => {
    if (e.pointerType === "touch" || !drag.current) return;
    const was = drag.current.mode;
    if (moveTo(e.clientX, e.clientY, e.timeStamp) && was === "pending" && drag.current?.mode === "sheet") e.currentTarget.setPointerCapture?.(e.pointerId);
  };
  const onUp = (e: PointerEvent<HTMLElement>) => {
    if (e.pointerType === "touch") return;
    finish(e.clientY, e.timeStamp);
  };
  // A drag that moved the sheet ends in a click on whatever it was let go over: that click is not a tap.
  // A click with no pointer behind it (detail 0: the keyboard, Switch Control) is never a drag's end.
  const onClickCapture = (e: MouseEvent<HTMLElement>) => {
    if (!dragged.current || e.detail === 0) return;
    dragged.current = false;
    e.preventDefault();
    e.stopPropagation();
  };
  const onHandleClick = () => {
    // peek, half, full, then back to peek: a tap alone reaches every height (and a lowered sheet comes back to its peek).
    set(snap === "peek" ? "half" : snap === "half" ? "full" : "peek");
  };
  const onKey = (e: KeyboardEvent<HTMLElement>) => {
    switch (e.key) {
      case "ArrowUp":
        e.preventDefault();
        set(stepSnap(snap, 1, lowerable));
        break;
      case "ArrowDown":
        e.preventDefault();
        set(stepSnap(snap, -1, lowerable));
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
  // Tapping the peek (the title and summary) opens the details, unless the tap was on something interactive inside it: those keep their own action.
  // Pointer convenience only; the handle is the keyboard and screen-reader way.
  const onPeekClick = (e: MouseEvent<HTMLDivElement>) => {
    if (snap === "full" || snap === "min") return;
    const t = e.target as Element | null;
    if (t?.closest(INTERACTIVE)) return;
    set("full");
  };
  const onRegionKey = (e: KeyboardEvent<HTMLElement>) => {
    // Escape from anywhere in the sheet folds it back to the peek.
    if (e.key === "Escape" && snap !== "peek") {
      e.stopPropagation();
      set("peek");
    }
  };

  const height = dragH ?? (snap === "min" ? minH : heights[snap]);
  const expanded = snap === "half" || snap === "full";
  // Map: from half and full back to the peek; on the short screen also from the peek down to the handle.
  const mapTo: Snap | null = expanded ? "peek" : snap === "peek" && lowerable ? "min" : null;
  return (
    <SheetClearance height={height} peek={heights.peek} dragging={dragH !== null}>
      {/* The room the sheet has: the same limits as its own fixed box, measured. */}
      <div ref={setRoomEl} data-plan-sheet-room aria-hidden="true" />
      {/* The room the open sheet has: the same, except on a very short screen, where it also takes the top bar's. */}
      <div ref={setFullRoomEl} data-plan-sheet-room data-plan-sheet-room-full aria-hidden="true" />
      <section
        ref={setSectionEl}
        aria-label={label}
        data-plan-sheet
        data-snap={snap}
        data-dragging={dragH !== null ? "true" : undefined}
        className={className}
        style={{ height, ["--sheet-h" as string]: `${height}px` }}
        onKeyDown={onRegionKey}
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerCancel={() => finish(null, null)}
        onClickCapture={onClickCapture}
      >
        <button
          ref={setHandleEl}
          type="button"
          data-plan-sheet-handle
          aria-expanded={expanded}
          aria-controls={contentId}
          aria-label={handleLabel(label, snap)}
          onClick={onHandleClick}
          onKeyDown={onKey}
        />
        {mapTo && (
          <button type="button" data-plan-sheet-map onClick={() => set(mapTo)}>
            Map
          </button>
        )}
        {peek != null && (
          // Pointer-only shortcut: the handle above is the keyboard and screen-reader control. Lowered, it is out of sight and inert.
          <div ref={setPeekEl} data-plan-sheet-peek onClick={onPeekClick} inert={snap === "min"}>
            {peek}
          </div>
        )}
        <div ref={bodyRef} id={contentId} data-plan-sheet-body tabIndex={-1} aria-label={`${label}, details`} role="group" inert={!expanded}>
          {children}
        </div>
      </section>
    </SheetClearance>
  );
}

// SheetClearance tells the page how tall the fixed sheet is right now (--plan-sheet-h on the root) and how tall its peek is (--plan-sheet-peek-h),
// so the page can leave that much room under its content (at a raised sheet the last card must still be scrollable above it), size the map to end
// at the peek, and keep what rides the sheet's edge (the radar strip) on it. data-plan-sheet-dragging on the root turns their transitions off while a
// finger moves the sheet. Removed again when the sheet goes away.
function SheetClearance({ height, peek, dragging, children }: { height: number; peek: number; dragging: boolean; children: ReactNode }) {
  useEffect(() => {
    document.documentElement.style.setProperty("--plan-sheet-h", `${height}px`);
  }, [height]);
  useEffect(() => {
    document.documentElement.style.setProperty("--plan-sheet-peek-h", `${peek}px`);
  }, [peek]);
  useEffect(() => {
    if (dragging) document.documentElement.setAttribute("data-plan-sheet-dragging", "");
    else document.documentElement.removeAttribute("data-plan-sheet-dragging");
  }, [dragging]);
  useEffect(
    () => () => {
      document.documentElement.style.removeProperty("--plan-sheet-h");
      document.documentElement.style.removeProperty("--plan-sheet-peek-h");
      document.documentElement.removeAttribute("data-plan-sheet-dragging");
    },
    [],
  );
  return <>{children}</>;
}
