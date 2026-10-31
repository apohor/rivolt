// The drive list's route thumbnails, fetched in batches. A row asks for its drive's thumbnail when it comes into view; the asks of one moment
// go out as one request of up to 60 ids (the server's cap), never one per row, and an answer is kept for the session so a row that scrolls out
// and back, or a list that re-renders, costs nothing. A drive the server had no route for is remembered too ("" below), so it is not asked again.
import { backend } from "./api";

const MAX_IDS = 60;
const FLUSH_MS = 40;

const known = new Map<string, string>(); // id -> polyline, or "" for none
const wanted = new Set<string>();
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setTimeout> | undefined;

function notify() {
  for (const l of listeners) l();
}

async function flush() {
  timer = undefined;
  const ids = [...wanted].filter((id) => !known.has(id));
  wanted.clear();
  for (let i = 0; i < ids.length; i += MAX_IDS) {
    const chunk = ids.slice(i, i + MAX_IDS);
    try {
      const got = await backend.routeThumbs(chunk);
      for (const id of chunk) known.set(id, got[id] ?? "");
    } catch {
      // A failed batch leaves its rows without a thumbnail this time; they ask again the next time they appear.
    }
  }
  notify();
}

// want registers a drive id; the thumbnail arrives through subscribe/get.
export function wantRouteThumb(id: string): void {
  if (known.has(id) || wanted.has(id)) return;
  wanted.add(id);
  timer ??= setTimeout(() => void flush(), FLUSH_MS);
}

export function routeThumb(id: string): string | undefined {
  return known.get(id);
}

export function subscribeRouteThumbs(cb: () => void): () => void {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

// forgetRouteThumbs drops what was learnt (a drive was edited, or the account changed).
export function forgetRouteThumbs(): void {
  known.clear();
  wanted.clear();
}
