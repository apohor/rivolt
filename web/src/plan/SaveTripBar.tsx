import { useState } from "react";

// SaveTripBar: Save trip, on the result itself. The only other place to save a trip is a name field on the
// Saved tab, away from the result, and the star next to each place saves a place, not the trip: people
// looked for Save on the plan and did not find it.
export function SaveTripBar({
  defaultName,
  activeName,
  saving,
  error,
  savedName,
  onSave,
  onShowSaved,
}: {
  defaultName: string;
  // The saved trip this result came from or was last saved as, if any: Save then updates it.
  activeName: string | null;
  saving: boolean;
  error: string | null;
  // The name it was just saved under, until the next change.
  savedName: string | null;
  onSave: (name: string) => void;
  onShowSaved: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  function begin() {
    setName(activeName ?? defaultName);
    setOpen(true);
  }
  return (
    <div data-save-trip className="rounded-lg border border-neutral-800 bg-neutral-950/40 px-3 py-2 text-sm">
      {open ? (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            const n = name.trim();
            if (!n) return;
            onSave(n);
            setOpen(false);
          }}
        >
          <label className="sr-only" htmlFor="save-trip-name">
            Name this trip
          </label>
          <input
            id="save-trip-name"
            type="text"
            value={name}
            autoFocus
            onChange={(e) => setName(e.target.value)}
            className="min-w-40 flex-1 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-100"
          />
          <button
            type="submit"
            disabled={saving || !name.trim()}
            className="rounded-md bg-emerald-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-800 disabled:opacity-50"
          >
            {activeName && activeName === name.trim() ? "Update" : "Save"}
          </button>
          <button type="button" onClick={() => setOpen(false)} className="text-xs text-neutral-400 hover:text-neutral-200">
            Cancel
          </button>
        </form>
      ) : (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <button
            type="button"
            onClick={begin}
            disabled={saving}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-100 hover:border-neutral-500 disabled:opacity-50"
          >
            {saving ? "Saving…" : activeName ? "Update saved trip" : "Save trip"}
          </button>
          {savedName && !error && (
            <span role="status" className="text-xs text-emerald-300">
              Saved as &ldquo;{savedName}&rdquo;.{" "}
              <button type="button" onClick={onShowSaved} className="underline hover:text-emerald-200">
                See Saved trips
              </button>
            </span>
          )}
        </div>
      )}
      {error && (
        <p role="alert" className="mt-2 text-xs text-red-400">
          Couldn&apos;t save the trip: {error}
        </p>
      )}
    </div>
  );
}
