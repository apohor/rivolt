import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { inDemo } from "../lib/demo";
import { Card } from "./ui";

// The card is about the R2 itself, so it is the one place that asks which model this is; every label
// and hidden row elsewhere goes by field_support alone.
export const isR2 = (model: string | undefined): boolean => (model ?? "").trim().toUpperCase() === "R2";

const DISMISS_KEY = "rivolt.r2.checked.dismissed";
export const R2_REPORT_URL = "https://github.com/apohor/rivolt/issues/new?template=r2-report.yml";

function readDismissed(): boolean {
  try {
    return window.localStorage.getItem(DISMISS_KEY) === "1";
  } catch {
    return false;
  }
}

// What has been checked on real R2s, in plain words (growth's copy). The one source for the Truck
// card and the Settings entry. Lists follow what the server's field_support says today.
export function R2CheckedContent() {
  return (
    <div className="space-y-3 text-sm text-neutral-300" data-r2-checked>
      <p>Rivolt supports the R2. Here is exactly what we&apos;ve confirmed on real R2s, so you know what to trust.</p>
      <div>
        <h4 className="text-xs font-medium uppercase tracking-wide text-neutral-400">Checked on real R2s</h4>
        <p className="mt-1">
          drives · charges and plug-in · charge limit · battery, range and odometer · location · cabin and battery
          temperature · tire pressure · sleep and power state · reported battery capacity
        </p>
      </div>
      <div>
        <h4 className="text-xs font-medium uppercase tracking-wide text-neutral-400">Shown as an estimate</h4>
        <p className="mt-1">
          time to full charge (Rivian&apos;s own countdown; labelled &quot;estimate&quot;). Power state can take a minute to appear
          after Rivolt reconnects.
        </p>
      </div>
      <div>
        <h4 className="text-xs font-medium uppercase tracking-wide text-neutral-400">Not on the R2 yet</h4>
        <p className="mt-1">
          trip charge target · why a charge stopped · camp and pet modes · Rivian&apos;s parked-energy breakdown ·
          smart-charging status. Each appears once we&apos;ve confirmed it on an R2.
        </p>
      </div>
      <p className="text-xs text-neutral-500">
        Outside temperature doesn&apos;t come through for any truck; Rivolt can look it up from weather data if you turn that on (off by default).
      </p>
      <p>
        <Link to="/r2" className="text-emerald-300 underline">
          Your first week with an R2
        </Link>
      </p>
      <p>
        <a href={R2_REPORT_URL} target="_blank" rel="noopener noreferrer" className="text-emerald-300 underline">
          Something looks wrong on your R2? Tell us
        </a>
      </p>
    </div>
  );
}

// The Truck tab card: dismissible once, and always reachable again from Settings → Vehicle.
export function R2CheckedCard() {
  const [dismissed, setDismissed] = useState(readDismissed);
  if (dismissed) return null;
  function dismiss() {
    setDismissed(true);
    try {
      window.localStorage.setItem(DISMISS_KEY, "1");
    } catch {
      /* private mode etc. - hides for this visit only */
    }
  }
  return (
    <Card
      title="Your R2 in Rivolt: what's been checked"
      span="full"
      actions={
        <button type="button" onClick={dismiss} className="text-xs text-neutral-400 hover:text-neutral-200">
          Got it
        </button>
      }
    >
      <R2CheckedContent />
    </Card>
  );
}

// Settings → Vehicle: the same content, always there for an account with an R2, even after the card
// on the Truck tab was dismissed.
export function R2CheckedSettings() {
  const status = useQuery({ queryKey: ["rivian", "status"], queryFn: () => backend.rivianStatus(), staleTime: 30_000, retry: 1 });
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    enabled: !!status.data?.authenticated || inDemo(),
    staleTime: 5 * 60_000,
    retry: 1,
  });
  if (!vehicles.data?.some((v) => isR2(v.model))) return null;
  return (
    <Card title="R2: what's been checked" id="r2-checked">
      <R2CheckedContent />
    </Card>
  );
}
