import { useState } from "react";

// A destructive or far-reaching action that needs an exact phrase typed before the button works.
// `children` sits above the phrase field (a reason input, say).
export function TypedConfirmDialog({
  title, detail, phrase, confirmLabel, onConfirm, onCancel, children,
}: {
  title: string;
  detail: string;
  phrase: string;
  confirmLabel: string;
  onConfirm: () => void;
  onCancel: () => void;
  children?: React.ReactNode;
}) {
  const [typed, setTyped] = useState("");
  const ok = typed.trim() === phrase;
  return (
    <div role="alertdialog" aria-label={title} data-typed-confirm className="rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
      <p className="font-medium text-rose-200">{title}</p>
      <p className="mt-1 text-xs text-neutral-300">{detail}</p>
      {children}
      <label className="mt-2 block text-xs text-neutral-300">
        Type <code className="rounded bg-neutral-900 px-1 text-neutral-100">{phrase}</code> to confirm
        <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" className="input mt-1" />
      </label>
      <div className="mt-3 flex gap-2">
        <button type="button" disabled={!ok} onClick={onConfirm} className="rounded-md bg-rose-700 px-3 py-1.5 text-sm font-semibold text-white hover:bg-rose-600 disabled:opacity-40">
          {confirmLabel}
        </button>
        <button type="button" onClick={onCancel} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500">
          Cancel
        </button>
      </div>
    </div>
  );
}
