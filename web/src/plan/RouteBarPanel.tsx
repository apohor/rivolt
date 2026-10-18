import { useId, useState, type ReactNode } from "react";

// The map-first planner's route bar (ui_theme_next). Always visible: From and
// To with a swap button, the departure chip, the starting SoC slider, and the
// Plan action. Everything else sits under a "More options" disclosure,
// collapsed by default, with a badge counting the options that are not at
// their defaults.
//
// The fields themselves (location search, departure picker, via stops, ...)
// are passed in as nodes, so this component owns the structure and behaviour
// and the page keeps owning the data. Styling hooks are data attributes on
// plain elements: data-plan-endpoints, data-plan-swap, data-plan-when,
// data-plan-soc, data-plan-more, data-plan-more-toggle, data-plan-more-body.
export function RouteBar({
  from,
  to,
  overnights,
  onSwap,
  when,
  soc,
  more,
  moreCount,
  submit,
}: {
  from: ReactNode;
  to: ReactNode;
  // The nights of the trip, right under From and To: the hint, each overnight, and the add button.
  overnights?: ReactNode;
  onSwap: () => void;
  // The departure chip ("Now" or the picked time) and its popovers.
  when: ReactNode;
  soc: {
    // The typed value; "" means "use the live state".
    value: string;
    // The vehicle's live SoC, shown when value is empty (null: unknown).
    live: number | null;
    onChange: (v: string) => void;
  };
  // The disclosure body: via stops, overnight limits, target SoC, drive mode,
  // adapter, the charging tip.
  more: ReactNode;
  // How many of those are not at their defaults.
  moreCount: number;
  // The Plan action (data-plan-submit block with button and spinner).
  submit: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const moreId = useId();
  const typed = soc.value.trim() !== "" ? Number(soc.value) : null;
  const shown = typed != null && Number.isFinite(typed) ? typed : soc.live;
  const live = typed == null || !Number.isFinite(typed);
  return (
    <>
      <div data-plan-endpoints>
        {from}
        <button type="button" data-plan-swap aria-label="Swap From and To" onClick={onSwap}>
          <span aria-hidden>⇅</span>
        </button>
        {to}
      </div>
      {overnights && <div data-plan-nights>{overnights}</div>}
      <div data-plan-when>{when}</div>
      <div data-plan-soc>
        <div data-plan-soc-head>
          <span id={`${moreId}-soc`}>Starting SoC</span>
          <output aria-live="polite" htmlFor={`${moreId}-soc-range`}>
            {shown == null ? "auto" : `${Math.round(shown)}%${live ? " (live)" : ""}`}
          </output>
          {!live && (
            <button type="button" data-plan-soc-reset onClick={() => soc.onChange("")}>
              Use live
            </button>
          )}
        </div>
        <input
          id={`${moreId}-soc-range`}
          type="range"
          min={5}
          max={100}
          step={1}
          aria-label="Starting SoC"
          aria-valuetext={shown == null ? "auto" : `${Math.round(shown)} percent${live ? ", live" : ""}`}
          value={shown ?? 80}
          onChange={(e) => soc.onChange(e.target.value)}
          className="ui-range"
          style={{ "--fill": `${Math.max(0, Math.min(100, (((shown ?? 80) - 5) / 95) * 100))}%` } as React.CSSProperties}
        />
      </div>
      <div data-plan-more data-open={open ? "true" : undefined}>
        <button
          type="button"
          data-plan-more-toggle
          aria-expanded={open}
          aria-controls={moreId}
          onClick={() => setOpen((v) => !v)}
        >
          <span>More options</span>
          {moreCount > 0 && (
            <span data-plan-more-count aria-label={`${moreCount} changed`}>
              {moreCount}
            </span>
          )}
        </button>
        <div id={moreId} data-plan-more-body hidden={!open}>
          {more}
        </div>
      </div>
      {submit}
    </>
  );
}
