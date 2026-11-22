import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { useDesignPreview } from "../lib/config";
import { planState } from "../lib/plan";
import { ProSheet, ProSheetPreload } from "./ProSheetLazy";

// Design key pro_entry (D4 A): a small "Pro tier" link in the header of every page, one tap to the Pro sheet. Only where the server bills and
// the account is on the free tier or its Pro ended; Pro and founding accounts, billing off and the demo never see it. It is a plain link: it
// never pulses, changes colour or carries a count or a date. Key off: nothing renders.
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
        className="rounded-md px-2 py-1 text-sm text-neutral-300 hover:text-neutral-100 hover:underline"
        onClick={() => setOpen(true)}
      >
        Pro tier
      </button>
      {open && <ProSheet state={state} onClose={() => setOpen(false)} />}
    </>
  );
}
