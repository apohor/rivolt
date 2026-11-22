import { lazy, Suspense, useEffect, useState, type ComponentProps } from "react";
import { track } from "../lib/track";

// The Pro sheet is opened rarely, so it is its own chunk. Whatever shows a button that opens it mounts <ProSheetPreload/> beside it: the chunk
// is fetched then (and kept by the browser), so the click opens the sheet at once, with no flash, and still works offline after load.
const load = () => import("./ProSheet");
const Sheet = lazy(() => load().then((m) => ({ default: m.ProSheet })));

export const ProSheetPreload = () => {
  useEffect(() => void load().catch(() => {}), []);
  return null;
};

// While the chunk is on its way (a slow line, or a click before the preload landed) the button that was pressed is marked aria-busy and
// data-sheet-pending; the stylesheet dims only the latter and shows the progress cursor, so the press visibly did something and no other busy
// button is touched.
const Pending = ({ opener }: { opener: HTMLElement | null }) => {
  useEffect(() => {
    opener?.setAttribute("aria-busy", "true");
    opener?.setAttribute("data-sheet-pending", "");
    return () => {
      opener?.removeAttribute("aria-busy");
      opener?.removeAttribute("data-sheet-pending");
    };
  }, [opener]);
  return null;
};

// What opened the sheet: the control that was pressed (a click does not focus a button in Safari, so focus alone would miss it), else what has focus.
function openerNow(): HTMLElement | null {
  const pressed = (window.event?.target as Element | null | undefined)?.closest?.("button, a[href], [role=button]");
  if (pressed instanceof HTMLElement) return pressed;
  const a = document.activeElement;
  return a instanceof HTMLElement && a !== document.body ? a : null;
}

// The opener is read when the sheet is first rendered, in the click that opens it, so it is known even if the chunk arrives later; the sheet
// moves focus into itself when it mounts and gives it back to this element on close.
export const ProSheet = (props: Omit<ComponentProps<typeof Sheet>, "opener">) => {
  const [opener] = useState(openerNow);
  // One count per opening, on the person's tap (this mounts only when something they pressed opens the sheet; never in the background).
  // The event carries the entry point from the fixed list and nothing else; track() sends nothing under Do Not Track or GPC.
  useEffect(() => track("pro_sheet_open", { from: props.from }), [props.from]);
  return (
    <Suspense fallback={<Pending opener={opener} />}>
      <Sheet {...props} opener={opener} />
    </Suspense>
  );
};
