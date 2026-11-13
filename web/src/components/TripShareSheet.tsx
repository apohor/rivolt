import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type SavedTrip, type TripShareCreated } from "../lib/api";
import { SHARE_EXPIRY, SHARE_TITLE_MAX, shareCreateMessage, shareLinkState } from "../lib/tripShare";

const ring = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-300";
const btn = `rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50 ${ring}`;
const solid = `rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50 ${ring}`;

const day = (iso: string) => new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });

// Share a saved trip as a link. A link shows a frozen copy of the plan to anyone who has it, so the sheet says what is in it before the button.
// The link is shown once (the server keeps only a hash of it); to share again, make a new one. The native dialog gives the focus trap, Escape
// and the return of focus to the Share button.
export function TripShareSheet({ trip, onClose }: { trip: SavedTrip; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement | null>(null);
  const qc = useQueryClient();
  const [title, setTitle] = useState("");
  const [days, setDays] = useState<1 | 7 | 30 | null>(30);
  const [made, setMade] = useState<TripShareCreated | null>(null);
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal();
  }, []);
  const key = ["trip-shares", trip.id];
  const links = useQuery({ queryKey: key, queryFn: () => backend.tripShareList(trip.id), select: (r) => r.links });
  const create = useMutation({
    mutationFn: () => backend.tripShareCreate(trip.id, { title: title.trim() || "Shared trip", ...(days ? { expires_in_days: days } : {}) }),
    onSuccess: (c) => {
      setMade(c);
      setCopied(false);
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) => backend.tripShareRevoke(id),
    onSuccess: (_d, id) => {
      if (made?.id === id) setMade(null);
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const url = made ? window.location.origin + made.path : "";
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };
  return (
    <dialog
      ref={ref}
      onClose={onClose}
      onClick={(e) => {
        if (e.target === ref.current) ref.current?.close();
      }}
      aria-labelledby="trip-share-title"
      data-testid="trip-share-sheet"
      className="m-auto w-[min(32rem,calc(100vw-1.5rem))] rounded-xl border border-neutral-800 bg-neutral-950 p-0 text-neutral-200 backdrop:bg-black/60"
    >
      <div className="max-h-[85vh] space-y-4 overflow-y-auto p-5">
        <div className="flex items-start justify-between gap-3">
          <h2 id="trip-share-title" className="text-lg font-semibold text-neutral-50">Share this trip</h2>
          <button type="button" onClick={() => ref.current?.close()} aria-label="Close" className={`-mr-2 -mt-1 min-h-11 min-w-11 rounded-md text-neutral-400 hover:text-neutral-100 ${ring}`}>
            ✕
          </button>
        </div>
        <p className="text-sm text-neutral-300" data-testid="trip-share-says">
          Anyone with the link can open a read-only copy of this plan, with no account. It shows the route, the charging stops (public chargers, with their names), times and battery levels. A stop you added yourself
          appears as an unnamed, approximate point, with its arrival and departure times and battery levels. It shows the <strong>start and destination areas</strong> (not
          the exact start or destination: the first and last few kilometers of the route are left out) and
          the <strong>departure date and time</strong>. It does not show your name, your vehicle, costs, or the saved trip's name. Later edits to the trip do not change the link. We do not record who opens it.
        </p>
        {made ? (
          <div className="space-y-2" data-testid="trip-share-made">
            <label className="block text-xs text-neutral-400" htmlFor="trip-share-url">Your link. This is the only time it is shown; to share again, make a new link.</label>
            <div className="flex gap-2">
              <input id="trip-share-url" readOnly value={url} onFocus={(e) => e.currentTarget.select()} className={`min-w-0 flex-1 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-100 ${ring}`} />
              <button type="button" onClick={copy} className={btn}>{copied ? "Copied" : "Copy"}</button>
            </div>
            <button type="button" onClick={() => { setMade(null); create.reset(); }} className="text-xs text-neutral-400 underline underline-offset-2 hover:text-neutral-200">Make another link</button>
          </div>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
            className="space-y-3"
          >
            <div>
              <label htmlFor="trip-share-name" className="block text-xs text-neutral-400">Title on the link (anyone with the link sees it)</label>
              <input
                id="trip-share-name"
                value={title}
                maxLength={SHARE_TITLE_MAX}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="Shared trip"
                className={`mt-1 w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-100 placeholder-neutral-600 ${ring}`}
              />
            </div>
            <fieldset>
              <legend className="text-xs text-neutral-400">The link works for</legend>
              <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1">
                {SHARE_EXPIRY.map((o) => (
                  <label key={o.label} className="flex min-h-8 items-center gap-1.5 text-sm text-neutral-200">
                    <input type="radio" name="trip-share-expiry" checked={days === o.days} onChange={() => setDays(o.days)} />
                    {o.label}
                  </label>
                ))}
              </div>
            </fieldset>
            {create.isError && <p role="alert" className="text-sm text-amber-300" data-testid="trip-share-error">{shareCreateMessage(create.error)}</p>}
            <button type="submit" disabled={create.isPending} className={solid}>{create.isPending ? "Making the link…" : "Create link"}</button>
          </form>
        )}
        <div>
          <h3 className="text-sm font-medium text-neutral-100">Links for this trip</h3>
          {links.isLoading ? (
            <p className="mt-1 text-xs text-neutral-500">Loading…</p>
          ) : (links.data ?? []).length === 0 ? (
            <p className="mt-1 text-xs text-neutral-500">None yet.</p>
          ) : (
            <ul className="mt-1 space-y-1.5" data-testid="trip-share-list">
              {(links.data ?? []).map((l) => {
                const st = shareLinkState(l);
                return (
                  <li key={l.id} className="flex items-center gap-2 rounded-md border border-neutral-800 px-3 py-1.5 text-sm">
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-neutral-100">{l.title}</span>
                      <span className="block text-xs text-neutral-500">
                        {st === "revoked" ? "Turned off" : st === "expired" ? `Expired ${day(l.expires_at as string)}` : l.expires_at ? `Made ${day(l.created_at)}, works until ${day(l.expires_at)}` : `Made ${day(l.created_at)}, works until you turn it off`}
                      </span>
                    </span>
                    {st === "live" && (
                      <button type="button" disabled={revoke.isPending} onClick={() => revoke.mutate(l.id)} className={`${btn} shrink-0`} aria-label={`Turn off the link "${l.title}"`}>
                        Turn off
                      </button>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          {revoke.isError && <p role="alert" className="mt-1 text-sm text-amber-300">Couldn't turn the link off. Try again.</p>}
        </div>
      </div>
    </dialog>
  );
}
