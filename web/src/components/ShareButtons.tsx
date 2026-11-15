import { useCallback, useEffect, useState } from "react";
import { backend, type Drive, type MilestoneItem } from "../lib/api";
import { useDesignPreview } from "../lib/config";
import { inDemo } from "../lib/demo";
import { usePreferences } from "../lib/preferences";
import { buildMilestoneShareModel, buildTripShareModel, shareFileName } from "../lib/shareCard";
import { ShareSheet } from "./ShareSheet";

const link = "text-xs text-neutral-300 hover:text-emerald-300";

// "Share trip" on a drive: opens the sheet with a masked trip card (the day, distance, duration and efficiency;
// no places, and cost only if the user adds it).
export function ShareTripButton({ drive }: { drive: Drive }) {
  const { distanceUnit } = usePreferences();
  const [open, setOpen] = useState(false);
  const sample = inDemo();
  // The optional map (design-preview flag share_map): the server's trimmed route, fetched when the sheet opens. A drive
  // with no usable route (no_route, too_short, outside the free window) or a failed fetch simply has no toggle.
  const mapFlag = useDesignPreview("share_map");
  // Kept with the drive it belongs to: this button is reused as the page moves between drives, so another drive's route
  // is never taken for this one while its own is still loading.
  const [loaded, setLoaded] = useState<{ id: string; segments: string[] } | null>(null);
  useEffect(() => {
    if (!open || !mapFlag || sample) return;
    const ac = new AbortController();
    setLoaded(null);
    backend
      .drivePostcard(drive.ID, ac.signal)
      .then((p) => setLoaded(p.route.segments.length > 0 ? { id: drive.ID, segments: p.route.segments } : null))
      .catch(() => setLoaded(null));
    return () => ac.abort();
  }, [open, mapFlag, sample, drive.ID]);
  const segments = loaded && loaded.id === drive.ID ? loaded.segments : null;
  const build = useCallback(
    (includeCost: boolean, _includeOffpeak: boolean, includeMap: boolean) => buildTripShareModel(drive, { includeCost, sample, mapSegments: includeMap ? segments : null, unit: distanceUnit }),
    [drive, sample, segments, distanceUnit],
  );
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} className={link}>
        Share trip
      </button>
      {open && <ShareSheet build={build} fileName={shareFileName("trip", drive.StartedAt)} withCostToggle withMapToggle={segments !== null} onClose={() => setOpen(false)} />}
    </>
  );
}

// "Share" on a milestone: the server's title and line as a card (never a dollar figure, so no cost toggle).
export function ShareMilestoneButton({ milestone }: { milestone: MilestoneItem }) {
  const [open, setOpen] = useState(false);
  const sample = inDemo();
  const build = useCallback(() => buildMilestoneShareModel(milestone, { sample }), [milestone, sample]);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} className={link}>
        Share
      </button>
      {open && <ShareSheet build={build} fileName={shareFileName("milestone", milestone.achieved_at)} withCostToggle={false} onClose={() => setOpen(false)} />}
    </>
  );
}
