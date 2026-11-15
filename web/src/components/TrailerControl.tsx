import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { backend, type Drive } from "../lib/api";
import { driveHasTrailer } from "../lib/drives";
import { Card } from "./ui";

// What the user can say about a drive's trailer: nothing (use what the truck reported), yes, or no.
type Choice = "auto" | "yes" | "no";

export function choiceOf(override: boolean | null | undefined): Choice {
  return override == null ? "auto" : override ? "yes" : "no";
}
export function overrideOf(c: Choice): boolean | null {
  return c === "auto" ? null : c === "yes";
}
function recordedLabel(t: boolean | null | undefined): string {
  return t == null ? "the truck did not say" : t ? "the truck reported a trailer" : "the truck reported no trailer";
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
  const value = m.isPending && m.variables ? m.variables : choiceOf(drive.TrailerOverride);
  return { m, value };
}

export function TrailerControl({ drive }: { drive: Pick<Drive, "ID" | "Trailer" | "TrailerOverride"> }) {
  const { m, value } = useTrailerChoice(drive);
  return (
    <Card>
      <label className="flex flex-wrap items-center justify-between gap-3 text-sm" data-trailer-control>
        <span>Trailer</span>
        <select
          value={value}
          disabled={m.isPending}
          onChange={(e) => m.mutate(e.target.value as Choice)}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm"
        >
          <option value="auto">Automatic ({recordedLabel(drive.Trailer)})</option>
          <option value="yes">Trailer connected</option>
          <option value="no">No trailer</option>
        </select>
      </label>
      <p className="mt-2 text-xs text-neutral-500">
        A drive with a trailer is left out of the efficiency figures. The truck reports a trailer connector, not what is on it, so a bike rack
        or cargo carrier with lights can read as a trailer.
      </p>
      {m.isError && <p className="mt-2 text-xs text-red-400">Could not save that. Try again.</p>}
    </Card>
  );
}

// The trailer_inline form: one line under the title, "Trailer: none · change", instead of a card. "change" opens a small menu of the same three
// choices, saved the same way. Without `canChange` (the demo) it is the line alone.
export function TrailerInline({ drive, canChange }: { drive: Pick<Drive, "ID" | "Trailer" | "TrailerOverride">; canChange: boolean }) {
  const { m, value } = useTrailerChoice(drive);
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
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
    <span ref={root} data-trailer-inline className="relative inline-flex flex-wrap items-center gap-x-1.5 text-xs text-neutral-400">
      <span data-drive-trailer>{has ? "Trailer: yes (not in efficiency)" : "Trailer: none"}</span>
      {canChange && (
        <>
          <span aria-hidden>·</span>
          <button
            type="button"
            data-trailer-change
            aria-haspopup="menu"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
            className="-my-2 py-2 text-neutral-300 underline underline-offset-2 hover:text-neutral-100"
          >
            change
          </button>
          {open && (
            <div role="menu" aria-label="Trailer" className="absolute left-0 top-full z-20 mt-1 w-64 rounded-lg border border-neutral-700 bg-neutral-900 p-1 shadow-lg">
              {items.map(([c, label]) => (
                <button
                  key={c}
                  type="button"
                  role="menuitemradio"
                  aria-checked={value === c}
                  disabled={m.isPending}
                  onClick={() => {
                    setOpen(false);
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
      {m.isError && <span role="alert" className="basis-full text-red-400">Could not save that. Try again.</span>}
    </span>
  );
}
