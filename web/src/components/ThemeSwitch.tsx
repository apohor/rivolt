import { useThemeChoice, type ThemeChoice } from "../lib/theme";

// The header's Auto / Light / Dark switch: one tap from every page. Shares useThemeChoice with
// Settings → Appearance, so the two stay in sync. "Auto" is the stored "system" choice.
const OPTIONS: { key: ThemeChoice; label: string; icon: React.ReactNode }[] = [
  {
    key: "system",
    label: "Auto",
    icon: (
      <>
        <circle cx="12" cy="12" r="8" />
        <path d="M12 4a8 8 0 0 1 0 16z" fill="currentColor" />
      </>
    ),
  },
  {
    key: "light",
    label: "Light",
    icon: (
      <>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4" />
      </>
    ),
  },
  {
    key: "dark",
    label: "Dark",
    icon: <path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" />,
  },
];

function Icon({ children }: { children: React.ReactNode }) {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  );
}

export function ThemeSwitch() {
  const [choice, setChoice] = useThemeChoice();
  const current = OPTIONS.find((o) => o.key === choice) ?? OPTIONS[0];
  const next = OPTIONS[(OPTIONS.indexOf(current) + 1) % OPTIONS.length];
  // A radio group is one tab stop; the arrow keys move the choice (and the focus) within it.
  const onArrow = (e: React.KeyboardEvent<HTMLButtonElement>) => {
    const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const to = OPTIONS[(OPTIONS.indexOf(current) + step + OPTIONS.length) % OPTIONS.length];
    setChoice(to.key);
    const group = e.currentTarget.parentElement;
    requestAnimationFrame(() => group?.querySelector<HTMLButtonElement>('[aria-checked="true"]')?.focus());
  };
  return (
    <>
      <div
        role="radiogroup"
        aria-label="Theme"
        data-theme-switch
        className="inline-flex shrink-0 rounded-full border border-neutral-800 bg-neutral-900/60 p-0.5 max-[200px]:hidden"
      >
        {OPTIONS.map((o) => (
          <button
            key={o.key}
            type="button"
            role="radio"
            aria-checked={choice === o.key}
            aria-label={o.label}
            title={o.label}
            tabIndex={choice === o.key ? 0 : -1}
            onClick={() => setChoice(o.key)}
            onKeyDown={onArrow}
            className={`flex h-8 w-8 items-center justify-center rounded-full transition-colors ${
              choice === o.key ? "bg-neutral-700 text-emerald-300" : "text-neutral-400 hover:text-neutral-100"
            }`}
          >
            <Icon>{o.icon}</Icon>
          </button>
        ))}
      </div>
      {/* Too narrow for three buttons (a phone at Safari's aA 300 %): one button that cycles Auto, Light, Dark. */}
      <button
        type="button"
        data-theme-cycle
        aria-label={`Theme: ${current.label}. Switch to ${next.label}`}
        onClick={() => setChoice(next.key)}
        className="hidden h-9 w-9 shrink-0 items-center justify-center rounded-full border border-neutral-800 text-neutral-300 max-[200px]:flex"
      >
        <Icon>{current.icon}</Icon>
      </button>
    </>
  );
}
