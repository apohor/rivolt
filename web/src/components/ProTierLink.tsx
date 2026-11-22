import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { useDesignPreview } from "../lib/config";
import { planState } from "../lib/plan";
import { ProSheet, ProSheetPreload } from "./ProSheetLazy";

// Design key pro_entry (D4 A): a small "Pro tier" link in the header of every page, one tap to the Pro sheet. Only where the server bills and
// the account is on the free tier or its Pro ended; Pro and founding accounts, billing off and the demo never see it. It is a small outline
// pill with a star (mock m20a), left of the person button; under 360 px it is the star alone. It never pulses, changes colour or carries a count or a date. Key off: nothing renders.
export function ProTierLink() {
  const on = useDesignPreview("pro_entry");
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000, enabled: on });
  const [open, setOpen] = useState(false);
  const state = planState(me.data);
  if (!on || (state.kind !== "free" && state.kind !== "lapsed")) return null;
  return (
    <>
      <ProSheetPreload />
      <button
        type="button"
        data-pro-tier-link
        className="flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full border border-neutral-700 px-[11px] py-1 text-[13px] font-medium text-neutral-300 hover:border-neutral-500 hover:text-neutral-100 max-[359px]:px-2"
        onClick={() => setOpen(true)}
      >
        <span aria-hidden="true" className="text-[11px] text-emerald-400">{"\u2726"}</span>
        {/* Under 360 px only the star stays; "Pro tier" remains the accessible name. */}
        <span className="max-[359px]:sr-only">Pro tier</span>
      </button>
      {open && <ProSheet from="header" state={state} onClose={() => setOpen(false)} />}
    </>
  );
}
