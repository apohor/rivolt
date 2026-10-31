import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { planState } from "../lib/plan";
import { proLimitText, type ProLimit } from "../lib/proLimit";
import { ProSheet } from "./ProSheet";

// What a screen shows instead of an error when the server answers a free account's limit (HTTP 402): why, that nothing is lost, and
// the way to Pro. Without a billing server (no plan screens) it only says why.
export function ProLimitNotice({ limit }: { limit: ProLimit }) {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const state = planState(me.data);
  const [open, setOpen] = useState(false);
  return (
    <div role="note" data-pro-limit={limit.code} className="rounded-lg border border-sky-800/60 bg-sky-950/40 px-3 py-3 text-sm text-sky-100">
      <p>{proLimitText(limit)}</p>
      {state.kind !== "off" && (
        <button type="button" className="mt-2 rounded-md border border-sky-700 px-3 py-1.5 text-sm hover:border-sky-500" onClick={() => setOpen(true)}>
          See Pro
        </button>
      )}
      {open && <ProSheet state={state} onClose={() => setOpen(false)} />}
    </div>
  );
}
