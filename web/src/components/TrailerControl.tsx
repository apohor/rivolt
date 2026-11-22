import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { backend, type Drive } from "../lib/api";
import { driveHasTrailer } from "../lib/drives";

// What the user can say about a drive's trailer: nothing (use what the truck reported), yes, or no.
type Choice = "auto" | "yes" | "no";

export function choiceOf(override: boolean | null | undefined): Choice {
  return override == null ? "auto" : override ? "yes" : "no";
}
export function overrideOf(c: Choice): boolean | null {
  return c === "auto" ? null : c === "yes";
}
function recordedLabel(t: boolean | null | undefined): string {
  return t == null
    ? "the truck did not say"
    : t
      ? "the truck reported a trailer"
      : "the truck reported no trailer";
}

// Lets the user correct whether a trailer was connected on a drive. A drive with a trailer is left out of the efficiency
// figures, so a wrong guess either way skews them. The signal is the connector, not the load.
function useTrailerChoice(drive: Pick<Drive, "ID" | "TrailerOverride">) {
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: (c: Choice) => backend.driveTrailer(drive.ID, overrideOf(c)),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["drives"] });
      void qc.invalidateQueries({ queryKey: ["insights"] });
    },
  });
  const value =
    m.isPending && m.variables ? m.variables : choiceOf(drive.TrailerOverride);
  return { m, value };
}

// One line under the title, "Trailer: none · change", instead of a card. "change" opens a small menu of the same three
// choices, saved the same way. Without `canChange` (the demo) it is the line alone.
export function TrailerInline({
  drive,
  canChange,
}: {
  drive: Pick<Drive, "ID" | "Trailer" | "TrailerOverride">;
  canChange: boolean;
}) {
  const { m, value } = useTrailerChoice(drive);
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  const changeBtn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) changeBtn.current?.focus();
  };
  const move = (to: (i: number, n: number) => number) => {
    const els = Array.from(
      menu.current?.querySelectorAll<HTMLButtonElement>(
        "[role=menuitemradio]",
      ) ?? [],
    );
    if (!els.length) return;
    const at = els.indexOf(document.activeElement as HTMLButtonElement);
    els[to(at, els.length)]?.focus();
  };
  useEffect(() => {
    if (!open) return;
    const els = menu.current?.querySelectorAll<HTMLButtonElement>(
      "[role=menuitemradio]",
    );
    (
      menu.current?.querySelector<HTMLButtonElement>("[aria-checked=true]") ??
      els?.[0]
    )?.focus();
    const away = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") close(true);
    };
    document.addEventListener("mousedown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", away);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);
  const has = driveHasTrailer(drive) === true;
  const items: [Choice, string][] = [
    ["auto", `Automatic (${recordedLabel(drive.Trailer)})`],
    ["yes", "Yes, a trailer was connected"],
    ["no", "No trailer"],
  ];
  return (
    <span
      ref={root}
      data-trailer-inline
      className="relative inline-flex flex-wrap items-center gap-x-1.5 text-xs text-neutral-400"
    >
      <span data-drive-trailer>
        {has ? "Trailer: yes (not in efficiency)" : "Trailer: none"}
      </span>
      {canChange && (
        <>
          <span aria-hidden>·</span>
          <button
            type="button"
            data-trailer-change
            ref={changeBtn}
            aria-haspopup="menu"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
            className="-my-2 py-2 text-neutral-300 underline underline-offset-2 hover:text-neutral-100"
          >
            change
          </button>
          {open && (
            <div
              ref={menu}
              role="menu"
              aria-label="Trailer"
              onKeyDown={(e) => {
                if (e.key === "ArrowDown") move((i, n) => (i + 1) % n);
                else if (e.key === "ArrowUp") move((i, n) => (i - 1 + n) % n);
                else if (e.key === "Home") move(() => 0);
                else if (e.key === "End") move((_, n) => n - 1);
                else if (e.key === "Tab") return close(false);
                else return;
                e.preventDefault();
              }}
              className="absolute left-0 top-full z-20 mt-1 w-64 rounded-lg border border-neutral-700 bg-neutral-900 p-1 shadow-lg"
            >
              {items.map(([c, label]) => (
                <button
                  key={c}
                  type="button"
                  role="menuitemradio"
                  aria-checked={value === c}
                  tabIndex={-1}
                  aria-disabled={m.isPending || undefined}
                  onClick={() => {
                    // aria-disabled, not disabled: a disabled button can't take focus, so focus on open would land nowhere while a save is pending.
                    if (m.isPending) return;
                    close(true);
                    if (c !== value) m.mutate(c);
                  }}
                  className="flex min-h-[40px] w-full items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left text-sm text-neutral-200 hover:bg-neutral-800"
                >
                  <span>{label}</span>
                  {value === c && <span aria-hidden>✓</span>}
                </button>
              ))}
            </div>
          )}
        </>
      )}
      {m.isError && (
        <span role="alert" className="basis-full text-red-400">
          Could not save that. Try again.
        </span>
      )}
    </span>
  );
}
