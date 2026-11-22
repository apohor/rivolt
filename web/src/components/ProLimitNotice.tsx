import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { useDesignPreview } from "../lib/config";
import { planState } from "../lib/plan";
import { PLANNER_NOTE_V2, proFeatureText, proLimitText, type ProFeature, type ProLimit } from "../lib/proLimit";
import { ProSheet, ProSheetPreload } from "./ProSheetLazy";
import { DemoLinks } from "./DemoLinks";
import { demoLinksFor } from "../lib/demoLinks";

// What a screen shows instead of an error when the server answers a free account's limit (HTTP 402): why, that nothing is lost, and
// the way to Pro. Without a billing server (no plan screens) it only says why.
//
// `privacy` keeps the privacy controls in reach on a record the free window hides (they are free on every plan, nothing is deleted):
// the export, "delete history before a month", and, where the record can be deleted by itself, that button (`extra`).
export function ProLimitNotice({ limit, privacy = false, extra, feature }: { limit: ProLimit; privacy?: boolean; extra?: ReactNode; feature?: ProFeature }) {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const state = planState(me.data);
  const [open, setOpen] = useState(false);
  // pro_entry (D5 A): the planner note and its button read as the mock says; every other notice is unchanged.
  const entryV2 = useDesignPreview("pro_entry") && feature === "planner";
  return (
    <div role="note" data-pro-limit={limit.code} className="rounded-lg border border-sky-800/60 bg-sky-950/40 px-3 py-3 text-sm text-sky-100">
      <p>{entryV2 ? PLANNER_NOTE_V2 : feature ? proFeatureText(feature, limit.code === "pro_required" ? limit.limit : undefined) : proLimitText(limit)}</p>
      {privacy && (
        <div data-pro-limit-privacy className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
          <Link to="/settings#export" className="underline underline-offset-2">Export everything</Link>
          <Link to="/settings#delete-history" className="underline underline-offset-2">Delete history before a month</Link>
          {extra}
        </div>
      )}
      {state.kind !== "off" && <DemoLinks links={demoLinksFor({ feature, code: limit.code })} className="mt-2" />}
      {state.kind !== "off" && <ProSheetPreload />}
      {state.kind !== "off" && (
        <button type="button" className="mt-2 rounded-md border border-sky-700 px-3 py-1.5 text-sm hover:border-sky-500" onClick={() => setOpen(true)}>
          {entryV2 ? "See the Pro tier" : "See Pro"}
        </button>
      )}
      {open && <ProSheet state={state} onClose={() => setOpen(false)} />}
    </div>
  );
}
