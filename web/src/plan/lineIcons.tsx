// The planner form's line icons (plan_v2): the same 24-px outline style as the rest of the app, in the text colour, instead of emoji.
function Line({ children, size = 14 }: { children: React.ReactNode; size?: number }) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false" className="shrink-0">
      {children}
    </svg>
  );
}

export const HomeIcon = () => (
  <Line>
    <path d="M3 10.5 12 3l9 7.5" />
    <path d="M5 9.5V20h14V9.5" />
    <path d="M10 20v-6h4v6" />
  </Line>
);

export const CalendarIcon = () => (
  <Line>
    <rect x="3.5" y="5" width="17" height="15.5" rx="2" />
    <path d="M3.5 10h17M8 3v4M16 3v4" />
  </Line>
);

export const ClockIcon = () => (
  <Line>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7v5l3 2" />
  </Line>
);
