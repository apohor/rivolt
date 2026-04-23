import type { ReactNode } from "react";

export function Spinner() {
  return (
    <div className="flex items-center gap-2 text-sm text-neutral-400" role="status">
      <span
        className="inline-block h-3 w-3 animate-spin rounded-full border-2 border-neutral-600 border-t-neutral-200"
        aria-hidden
      />
      <span>loading…</span>
    </div>
  );
}

export function ErrorBox({ title, detail }: { title: string; detail?: string }) {
  return (
    <div
      role="alert"
      className="rounded-lg border border-rose-900 bg-rose-950/40 px-4 py-3 text-sm text-rose-200"
    >
      <div className="font-semibold">{title}</div>
      {detail ? <div className="mt-1 text-rose-300/80">{detail}</div> : null}
    </div>
  );
}

export function Card({
  title,
  children,
  actions,
  id,
}: {
  title?: string;
  children: ReactNode;
  actions?: ReactNode;
  id?: string;
}) {
  return (
    <section id={id} className="rounded-xl border border-neutral-800 bg-neutral-900/50 scroll-mt-20">
      {(title || actions) && (
        <header className="flex items-center justify-between border-b border-neutral-800 px-4 py-2.5">
          {title && <h2 className="text-sm font-medium text-neutral-200">{title}</h2>}
          {actions}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-6 flex items-start justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-neutral-400">{subtitle}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

// Toggle is a dark-mode-friendly switch. Use instead of <input type="checkbox">
// for boolean settings — the native control looks broken on neutral-950 and
// doesn't hint that it's a toggle (on/off) vs. a multi-select check.
export function Toggle({
  checked,
  onChange,
  id,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  id?: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      id={id}
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={[
        "relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border transition-colors",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500/60",
        "disabled:opacity-40 disabled:cursor-not-allowed",
        checked
          ? "bg-emerald-600 border-emerald-500"
          : "bg-neutral-800 border-neutral-700 hover:bg-neutral-700",
      ].join(" ")}
    >
      <span
        className={[
          "inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform",
          checked ? "translate-x-6" : "translate-x-1",
        ].join(" ")}
      />
    </button>
  );
}
