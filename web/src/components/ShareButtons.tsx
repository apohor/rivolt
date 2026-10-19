import { useCallback, useState } from "react";
import type { Drive, MilestoneItem } from "../lib/api";
import { inDemo } from "../lib/demo";
import { buildMilestoneShareModel, buildTripShareModel, shareFileName } from "../lib/shareCard";
import { ShareSheet } from "./ShareSheet";

const link = "text-xs text-neutral-300 hover:text-emerald-300";

// "Share trip" on a drive: opens the sheet with a masked trip card (the day, distance, duration and efficiency;
// no places, and cost only if the user adds it).
export function ShareTripButton({ drive }: { drive: Drive }) {
  const [open, setOpen] = useState(false);
  const sample = inDemo();
  const build = useCallback((includeCost: boolean) => buildTripShareModel(drive, { includeCost, sample }), [drive, sample]);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} className={link}>
        Share trip
      </button>
      {open && <ShareSheet build={build} fileName={shareFileName("trip", drive.StartedAt)} withCostToggle onClose={() => setOpen(false)} />}
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
