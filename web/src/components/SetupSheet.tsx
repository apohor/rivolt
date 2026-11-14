import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { useIosAppUrl } from "../lib/config";
import { pushSupported, subscribeThisDevice } from "../lib/push";
import { setupNeeded, parseRate, placeLabel, priceCitation, priceDefault, rateText, rememberSetupDismissed, truckPosition } from "../lib/setupSheet";
import { ChargeMap } from "../map/ChargeMap";

const ring = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-300";
const btn = `rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50 ${ring}`;
const solid = `rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50 ${ring}`;

type StepID = "home" | "price" | "notify" | "receipts";

// The sheet only for an account with something left to set (no home, or no home rate); one with both never sees it. Decided once, from
// the first answers, so saving home inside the sheet does not take it away mid-step.
export function SetupGate({ vehicleID, onClose }: { vehicleID: string; onClose: () => void }) {
  const home = useQuery({ queryKey: ["settings", "homeLocation"], queryFn: () => backend.homeLocationGet(), staleTime: 60_000, retry: 0 });
  const charging = useQuery({ queryKey: ["charging-settings"], queryFn: () => backend.getChargingSettings(), staleTime: 60_000, retry: 0 });
  const [need, setNeed] = useState<boolean | null>(null);
  if (need === null && home.data && charging.data) setNeed(setupNeeded(home.data, charging.data));
  return need ? <SetupSheet vehicleID={vehicleID} onClose={onClose} /> : null;
}

// The first-run setup sheet (design first-screen §3): home, electricity price, notifications and, where the operator allows it, Rivian
// charging history. Every step is one tap on a smart default and each has "Not now". It never saves anything until a button says so.
export function SetupSheet({ vehicleID, onClose }: { vehicleID: string; onClose: () => void }) {
  const receipts = useQuery({ queryKey: ["rivian-receipts"], queryFn: () => backend.rivianReceiptsGet(), retry: 0, staleTime: 60_000 });
  // Receipts are the operator's switch: where it is off the step does not exist.
  // (a failed read hides it too: a step that cannot say whether it is on has nothing to offer).
  const steps: StepID[] = receipts.data?.available === false || receipts.isError ? ["home", "price", "notify"] : ["home", "price", "notify", "receipts"];
  const [i, setI] = useState(0);
  const step = steps[Math.min(i, steps.length - 1)];
  const last = i >= steps.length - 1;
  const titleRef = useRef<HTMLHeadingElement | null>(null);
  const finish = () => {
    rememberSetupDismissed();
    onClose();
  };
  const next = () => (last ? finish() : setI(i + 1));
  // A stray tap on the backdrop or Escape closes it for this visit only; Close and finishing the steps are the answers that stick.
  const later = () => onClose();
  useEffect(() => {
    // The heading takes focus, not Close: a sheet that opens with a ring on its dismiss button reads as an error.
    titleRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") later();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return createPortal(
    <div className="fixed inset-0 z-[1200] flex items-end justify-center bg-black/60 p-0 sm:items-center sm:p-4" onClick={later}>
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="setup-title"
        data-setup-sheet
        className="max-h-[92vh] w-full max-w-md space-y-4 overflow-y-auto rounded-t-xl border border-neutral-800 bg-neutral-950 p-5 sm:rounded-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 id="setup-title" ref={titleRef} tabIndex={-1} className="text-lg font-semibold text-neutral-50 focus:outline-none">Set up Rivolt</h2>
            <p data-setup-progress className="mt-1 text-xs text-neutral-500">Step {Math.min(i, steps.length - 1) + 1} of {steps.length}. Each one is optional and can be changed later in Settings.</p>
          </div>
          <button type="button" className={btn} onClick={finish}>Close</button>
        </div>
        {step === "home" && <HomeStep vehicleID={vehicleID} onNext={next} />}
        {step === "price" && <PriceStep onNext={next} />}
        {step === "notify" && <NotifyStep onNext={next} />}
        {step === "receipts" && <ReceiptsStep onNext={next} />}
      </div>
    </div>,
    document.body,
  );
}

function StepFrame({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-3">
      <h3 className="text-sm font-semibold text-neutral-200">{title}</h3>
      {children}
    </section>
  );
}

// Step 1. The truck's own position is the offer; the pin and the place name show before anything is saved.
function HomeStep({ vehicleID, onNext }: { vehicleID: string; onNext: () => void }) {
  const qc = useQueryClient();
  const home = useQuery({ queryKey: ["settings", "homeLocation"], queryFn: () => backend.homeLocationGet(), staleTime: 60_000, retry: 0 });
  const state = useQuery({ queryKey: ["state", vehicleID], queryFn: () => backend.vehicleState(vehicleID), staleTime: 30_000, retry: 0 });
  const pos = truckPosition(state.data);
  const place = useQuery({
    queryKey: ["home-place", pos?.lat, pos?.lon],
    queryFn: () => backend.homePlace(pos!.lat, pos!.lon),
    enabled: !!pos && home.data?.set === false,
    staleTime: 5 * 60_000,
    retry: 0,
  });
  const save = useMutation({
    mutationFn: () =>
      backend.homeLocationPut({ set: true, latitude: pos!.lat, longitude: pos!.lon, label: "Home", address: placeLabel(place.data), source: "truck" }),
    onSuccess: (h) => {
      qc.setQueryData(["settings", "homeLocation"], h);
      qc.invalidateQueries({ queryKey: ["home-place-saved"] });
    },
  });
  if (home.isPending) return <p className="text-sm text-neutral-500">Loading…</p>;
  if (home.data?.set) {
    return (
      <StepFrame title="Home">
        <p data-setup-home-done className="text-sm text-neutral-300">
          Home is set{home.data.address ? `: ${home.data.address}` : ""}. You can change it in Settings.
        </p>
        <button type="button" className={solid} onClick={onNext}>Next</button>
      </StepFrame>
    );
  }
  return (
    <StepFrame title="Is your truck at home now?">
      {pos ? (
        <>
          <ChargeMap lat={pos.lat} lon={pos.lon} height={160} zoom={15} recorded />
          <p data-setup-place className="text-sm text-neutral-300">
            {place.isPending ? "Looking up the place…" : place.data ? placeLabel(place.data) : "The truck's current position"}
          </p>
          <p className="text-xs text-neutral-500">
            Home is where Rivolt tells home charges from away ones. It is saved exactly as the pin shows, and nothing is saved until you tap the button.
          </p>
          {save.isError && <p role="alert" className="text-sm text-red-300">Couldn't save home. Try again, or set it in Settings.</p>}
          <div className="flex flex-wrap gap-2">
            <button type="button" className={solid} disabled={save.isPending} onClick={() => save.mutate(undefined, { onSuccess: onNext })}>Set home here</button>
            <button type="button" className={btn} onClick={onNext}>Not now</button>
          </div>
        </>
      ) : (
        <>
          <p className="text-sm text-neutral-400">The truck hasn't reported a position yet. Set home later in Settings, or from a charge you made at home.</p>
          <button type="button" className={btn} onClick={onNext}>Next</button>
        </>
      )}
    </StepFrame>
  );
}

// Step 2. The state average is offered, dated and cited, and labelled an estimate; typing your own is always there.
function PriceStep({ onNext }: { onNext: () => void }) {
  const qc = useQueryClient();
  const charging = useQuery({ queryKey: ["charging-settings"], queryFn: () => backend.getChargingSettings(), staleTime: 60_000, retry: 0 });
  const home = useQuery({ queryKey: ["settings", "homeLocation"], queryFn: () => backend.homeLocationGet(), staleTime: 60_000, retry: 0 });
  const homeAt = home.data?.set ? { lat: home.data.latitude, lon: home.data.longitude } : null;
  const place = useQuery({
    queryKey: ["home-place-saved", homeAt?.lat, homeAt?.lon],
    queryFn: () => backend.homePlace(homeAt!.lat, homeAt!.lon),
    enabled: !!homeAt,
    staleTime: 5 * 60_000,
    retry: 0,
  });
  const price = useQuery({
    queryKey: ["state-price", place.data?.country_code, place.data?.admin1],
    queryFn: () => backend.statePrice(place.data!.country_code, place.data!.admin1),
    enabled: !!place.data?.country_code && !!place.data?.admin1,
    staleTime: 60 * 60_000,
    retry: 0,
  });
  const [typed, setTyped] = useState("");
  const [own, setOwn] = useState(false);
  const save = useMutation({
    mutationFn: (v: { rate: number; source?: "state_average" }) => backend.patchChargingRate({ home_price_per_kwh: v.rate, ...(v.source ? { home_price_source: v.source } : {}) }),
    onSuccess: (c) => {
      qc.setQueryData(["charging-settings"], c);
      onNext();
    },
  });
  if (charging.isPending || home.isPending) return <p className="text-sm text-neutral-500">Loading…</p>;
  const cur = charging.data?.home_currency || "USD";
  const existing = charging.data?.home_price_per_kwh ?? 0;
  if (existing > 0) {
    const est = charging.data?.home_price_source === "state_average";
    return (
      <StepFrame title="Electricity price">
        <p data-setup-price-done className="text-sm text-neutral-300">
          Your home rate is {rateText(existing, cur)}{est ? " (the state average, an estimate)" : ""}. You can change it in Settings.
        </p>
        <button type="button" className={solid} onClick={onNext}>Next</button>
      </StepFrame>
    );
  }
  const offer = priceDefault(place.data, price.data);
  const rate = parseRate(typed);
  return (
    <StepFrame title="What do you pay for electricity at home?">
      <p className="text-xs text-neutral-500">Used to price your home charges. Charges recorded before you set a rate stay unpriced.</p>
      {offer && !own && (
        <div data-setup-state-price className="space-y-2 rounded-lg border border-neutral-800 p-3">
          <p className="text-sm text-neutral-100">
            {rateText(offer.price_per_kwh, offer.currency)}
            <span className="ml-2 rounded-full border border-neutral-700 px-1.5 py-px text-[11px] text-neutral-400">estimated</span>
          </p>
          <p className="text-xs text-neutral-400">
            The {offer.name} average. {priceCitation(offer)}.{" "}
            <a href={offer.source.url} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-200">Source</a>
          </p>
          <p className="text-xs text-neutral-500">Charges priced with it are marked estimated in Money until you enter your own rate.</p>
          <div className="flex flex-wrap gap-2">
            <button type="button" className={solid} disabled={save.isPending} onClick={() => save.mutate({ rate: offer.price_per_kwh, source: "state_average" })}>
              Use {rateText(offer.price_per_kwh, offer.currency)}
            </button>
            <button type="button" className={btn} onClick={() => setOwn(true)}>Enter mine</button>
          </div>
        </div>
      )}
      {(!offer || own) && (
        <form
          className="space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (rate != null) save.mutate({ rate });
          }}
        >
          <label className="block text-sm text-neutral-300">
            Price per kWh ({cur})
            <input
              inputMode="decimal"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder="0.14"
              className="mt-1 block w-32 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
            />
          </label>
          {!offer && <p className="text-xs text-neutral-500">{homeAt ? "There is no state average to offer for this home, so type your rate." : "Set a home first to see your state's average, or type your rate."}</p>}
          <div className="flex flex-wrap gap-2">
            <button type="submit" className={solid} disabled={rate == null || save.isPending}>Save rate</button>
            <button type="button" className={btn} onClick={onNext}>Not now</button>
          </div>
        </form>
      )}
      {offer && !own && <button type="button" className="text-xs text-neutral-500 hover:text-neutral-300" onClick={onNext}>Not now</button>}
      {save.isError && <p role="alert" className="text-sm text-red-300">Couldn't save the rate. Try again, or set it in Settings.</p>}
    </StepFrame>
  );
}

// Step 3. Charging-complete and software-update only; the other kinds stay in Settings. Where the browser cannot push, it says so.
function NotifyStep({ onNext }: { onNext: () => void }) {
  const status = useQuery({ queryKey: ["push-status"], queryFn: () => backend.pushStatus(), retry: 0, staleTime: 60_000 });
  const appUrl = useIosAppUrl();
  const [state, setState] = useState<"idle" | "busy" | "on" | "blocked" | "error">("idle");
  if (!pushSupported() || (status.isSuccess && !status.data.enabled)) {
    return (
      <StepFrame title="Notifications">
        <p data-setup-push-unsupported className="text-sm text-neutral-400">
          Notifications aren't available in this browser. On an iPhone, add Rivolt to your Home Screen from the Share menu{appUrl ? ", or get the iPhone app" : ""}, then turn them on in Settings.
        </p>
        {appUrl && (
          <a href={appUrl} target="_blank" rel="noopener noreferrer" className="text-sm text-emerald-300 underline underline-offset-2">Get the iPhone app</a>
        )}
        <div><button type="button" className={solid} onClick={onNext}>Next</button></div>
      </StepFrame>
    );
  }
  async function enable() {
    setState("busy");
    try {
      const r = await subscribeThisDevice(
        { on_charging_done: true, on_plug_in_reminder: false, on_anomaly: false, on_software_update: true, on_milestones: false },
        { leaveExisting: true },
      );
      setState(r === "on" ? "on" : "blocked");
    } catch {
      setState("error");
    }
  }
  return (
    <StepFrame title="Know when charging is done">
      <p className="text-sm text-neutral-400">One notification when a charge finishes, and one when a software update is ready. Other kinds stay in Settings.</p>
      {state === "on" && <p data-setup-push-on className="text-sm text-emerald-300">Notifications are on for this device.</p>}
      {state === "blocked" && <p role="status" className="text-sm text-amber-300">This browser blocked notifications. Allow them in the site settings, then turn them on in Settings.</p>}
      {state === "error" && <p role="alert" className="text-sm text-red-300">Couldn't turn notifications on. Try again from Settings.</p>}
      <div className="flex flex-wrap gap-2">
        {state !== "on" && (
          <button type="button" className={solid} disabled={state === "busy"} onClick={() => void enable()}>Tell me when charging finishes</button>
        )}
        {state === "on" ? (
          <button type="button" className={solid} onClick={onNext}>Next</button>
        ) : (
          <button type="button" className={btn} onClick={onNext}>Not now</button>
        )}
      </div>
    </StepFrame>
  );
}

// Step 4. Receipts are off unless this button is pressed; the privacy line sits beside it.
function ReceiptsStep({ onNext }: { onNext: () => void }) {
  const qc = useQueryClient();
  const state = useQuery({ queryKey: ["rivian-receipts"], queryFn: () => backend.rivianReceiptsGet(), retry: 0, staleTime: 60_000 });
  const on = useMutation({
    mutationFn: () => backend.rivianReceiptsPut(true),
    onSuccess: (r) => {
      qc.setQueryData(["rivian-receipts"], r);
      onNext();
    },
  });
  if (state.data?.opt_in) {
    return (
      <StepFrame title="Your Rivian charging history">
        <p data-setup-receipts-done className="text-sm text-neutral-300">Charging history is on. You can turn it off in Settings, which deletes the stored receipts.</p>
        <button type="button" className={solid} onClick={onNext}>Done</button>
      </StepFrame>
    );
  }
  return (
    <StepFrame title="Your Rivian charging history">
      <p className="text-sm text-neutral-400">
        Rivolt can read the charging sessions Rivian has billed to your account: amount, energy, times, vendor and city. Public charging then shows what you actually paid.
      </p>
      <p className="text-xs text-neutral-500">
        Off unless you turn it on. Turning it off later in Settings deletes the stored receipts. We never ask for your payment card or address.{" "}
        <a href="/privacy" target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-300">Privacy</a>
      </p>
      {on.isError && <p role="alert" className="text-sm text-red-300">Couldn't turn it on. Try again from Settings.</p>}
      <div className="flex flex-wrap gap-2">
        <button type="button" className={solid} disabled={on.isPending} onClick={() => on.mutate()}>Show my charging history</button>
        <button type="button" className={btn} onClick={onNext}>Not now</button>
      </div>
    </StepFrame>
  );
}
