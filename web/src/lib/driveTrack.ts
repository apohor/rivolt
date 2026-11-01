// The drive map's line is the drive's samples in the order they arrived (At). But a sample's position is a GPS fix with its own time
// (LocationFixAt), and the truck's modem hands back fixes late and again: a sample can repeat the previous fix, or carry a fix OLDER than the
// one before it. Drawn in arrival order those older fixes jump the line back along the road and forward again, and the gradient line reads as a
// scribble. On a live drive a large share of the positions repeated the previous fix and a few were older than the one before.
//
// cleanTrack keeps a sample only when its fix is newer than the newest fix already kept, so the kept positions run forward in fix time: older
// fixes and exact repeats are dropped, and the order is the fixes' own. A sample with no fix time (a legacy row, an import) is taken to be
// fixed when it arrived. A fix stamped later than its own arrival is not believable (a fix cannot postdate the frame that carries it), so it is
// treated as no fix time; otherwise one bad stamp would hide every sample after it. Samples with no position pass through untouched.
//
// The server's recorder applies the same rule when it builds the stored route (lastFixTS in internal/rivian/recorder.go).

type TrackSample = { At: string; Lat: number; Lon: number; LocationFixAt?: string };

// A fix may be stamped this far after its frame's arrival (clock skew between the modem and the server) before it is not believed.
const FIX_AHEAD_SLACK_MS = 5000;

export function cleanTrack<T extends TrackSample>(samples: readonly T[]): T[] {
  // Arrival order, stable: the API sends them sorted, but this must not depend on it.
  const ordered = samples
    .map((s, i) => ({ s, i, at: Date.parse(s.At) }))
    .sort((a, b) => (a.at - b.at) || (a.i - b.i));
  let newest = -Infinity;
  const out: T[] = [];
  for (const { s, at } of ordered) {
    if (!hasPosition(s)) {
      out.push(s);
      continue;
    }
    let fix = s.LocationFixAt ? Date.parse(s.LocationFixAt) : NaN;
    if (!Number.isFinite(fix) || fix > at + FIX_AHEAD_SLACK_MS) fix = at;
    if (!Number.isFinite(fix)) {
      out.push(s); // no usable time at all: leave it where it is
      continue;
    }
    if (fix <= newest) continue; // an older fix, or the same one again
    newest = fix;
    out.push(s);
  }
  return out;
}

function hasPosition(s: TrackSample): boolean {
  return Number.isFinite(s.Lat) && Number.isFinite(s.Lon) && !(s.Lat === 0 && s.Lon === 0);
}
