import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router-dom";
import { ArcGauge } from "./ArcGauge";
import { PlugPicture } from "./PlugPicture";
import { CarSketch } from "./CarSketch";
import { useColdRange } from "./AmbientTempLine";
import { pickImageForNow, useIsLight, type PlacedImage } from "../lib/vehicleImage";
import { useAmbientTemp } from "../lib/ambientTemp";
import { useDesignPreview } from "../lib/config";
import { formatTemperature, usePreferences } from "../lib/preferences";
import { clock, type NowView } from "../lib/nowState";
import { conditionItems, type CondItem, type SlotWeather } from "./NowSlot";

// The redesigned Now hero (design_preview "now_v2"): a status chip, then one card with the gauge, the range,
// one line of context and the temperatures. Same data as the card it replaces; only the layout is new.

export function nowChip(
  v: NowView,
  asleepSinceMs: number | null,
  nowMs: number,
  pillText: string,
  // Design "now_tidy" with the state's slot: the slot already shows the power, the finish time and the speed, so the chip says the state only.
  terse = false,
): string {
  if (terse && v.kind === "charging") return "Charging";
  if (terse && v.kind === "driving") return "Driving";
  if (v.kind === "asleep" && asleepSinceMs != null)
    return `Asleep · since ${clock(asleepSinceMs)}`;
  if (v.kind === "charging") {
    const parts = ["Charging"];
    if (v.powerKw != null)
      parts.push(`${v.powerKw.toFixed(v.powerKw < 10 ? 1 : 0)} kW`);
    if (v.etaHours != null)
      parts.push(
        `${v.limitPct != null && v.limitPct < 100 ? "done" : "full"} by ${clock(nowMs + v.etaHours * 3_600_000)}`,
      );
    return parts.join(" · ");
  }
  if (v.kind === "driving")
    return v.speedMph != null
      ? `Driving · ${Math.round(v.speedMph)} mph`
      : "Driving";
  // Parked or plugged in: the state and since when. A stale or offline truck keeps the pill's own "Last seen ...".
  if (
    (v.kind === "parked" || v.kind === "plugged") &&
    !v.stale &&
    v.stateSinceMs != null
  )
    return `${v.kind === "plugged" ? "Plugged in" : "Parked"} · since ${clock(v.stateSinceMs)}`;
  // The chip names the truck's state, never "Live" (that word is the button to the live view).
  if (pillText === "Live")
    return v.kind === "plugged" ? "Plugged in" : v.kind === "asleep" ? "Asleep" : "Parked";
  return pillText;
}

export default function NowHeroV2({
  view,
  vehicleID,
  chip,
  pillTone,
  ambient,
  cabin,
  tone,
  rows,
  extra,
  footer,
  telemetryLink,
  critical,
  images,
  model,
  stateSlot,
  tidy = false,
  nowMs = 0,
  asleepTodayS = null,
  soft = false,
  graph = null,
  parkedRows = null,
}: {
  view: NowView;
  vehicleID: string;
  chip: string;
  pillTone: "live" | "warn" | "neutral";
  ambient?: SlotWeather | null;
  cabin: string | null;
  tone: string;
  rows: { k: string; v: string }[];
  extra: React.ReactNode;
  footer: React.ReactNode;
  telemetryLink: boolean;
  critical: boolean;
  // The vehicle's Rivian images; without any the line-art sketch of the model shows instead.
  images?: PlacedImage[];
  model?: string | null;
  // Design "now_slot": the state's figures in place of the picture (null keeps the picture).
  stateSlot?: React.ReactNode;
  // Design "now_tidy": one hierarchy (status row, battery and range, the state's slot, one row of conditions). Same data as without.
  tidy?: boolean;
  nowMs?: number;
  // While asleep: the seconds asleep today, shown in the conditions row.
  asleepTodayS?: number | null;
  // Design "now_charge_graph": the card in two balanced halves from 900 px (ring, range and figures left, the graph right).
  soft?: boolean;
  graph?: React.ReactNode;
  // Design "now_parked_graph" (soft, not charging): the figures as rows under the range from 900 px; the graph sits between the ring and the map on a phone.
  parkedRows?: React.ReactNode;
}) {
  const { temperatureUnit, distanceUnit } = usePreferences();
  const light = useIsLight();
  // Every distance on the card is in the user's unit (the truck reports miles).
  const dist = (mi: number) =>
    `${Math.round(distanceUnit === "km" ? mi * 1.609344 : mi)} ${distanceUnit}`;
  const amb = useAmbientTemp();
  const offline = view.kind === "offline";
  const charging = view.kind === "charging";
  const cold = useColdRange(
    vehicleID,
    amb.enabled && ambient ? ambient.temp_c : null,
  );
  const fullCharge =
    cold && !cold.reason && cold.range_low != null && cold.range_high != null
      ? Math.round((cold.range_low + cold.range_high) / 2)
      : null;
  // While charging: what the limit buys, scaled from the range the truck reports now.
  const atLimit =
    charging && view.limitPct != null && view.soc > 0 && view.rangeMi != null
      ? Math.round((view.rangeMi / view.soc) * view.limitPct)
      : null;
  // With the charging or driving slot the context is in the slot itself; the parked slot keeps the full-charge line and takes the outside
  // temperature into its weather row.
  const parkedSlot = stateSlot != null && (view.kind === "parked" || view.kind === "plugged" || view.kind === "asleep" || (view.kind === "offline" && view.sleeping));
  const subline = stateSlot && ((!parkedSlot && !soft) || view.kind === "driving") ? null : charging
    ? view.limitPct != null
      ? `Charging to ${Math.round(view.limitPct)}%${atLimit != null ? ` · ≈\u00a0${dist(atLimit)} when done` : ""}`
      : null
    : fullCharge != null
      ? `Full charge today ≈ ${fullCharge} ${distanceUnit}`
      : null;
  const outside =
    amb.enabled && ambient && !parkedSlot
      ? formatTemperature(ambient.temp_c, temperatureUnit)
      : null;
  // The chip sits in the page header's title row (Now ... chip), as in the design; inline when the page has no slot.
  // Design "plug_anim": the plug badge whenever the cable is in; the picture stays the whole truck.
  const plugPicture = useDesignPreview("plug_anim");
  const carUrl = pickImageForNow(images, view.kind, light);
  // A Rivian image that fails to load (moved or blocked) gives way to the drawn truck instead of a broken picture.
  const [failedUrl, setFailedUrl] = useState("");
  const [info, setInfo] = useState(false);
  const [slot, setSlot] = useState<Element | null>(null);
  useEffect(() => {
    setSlot(document.querySelector("[data-now-chip-slot]"));
  }, []);
  const chipEl = (
    <span
      data-testid="now-pill"
      data-now-chip
      className={`inline-flex items-center gap-2 rounded-full border px-3 py-1 text-sm ${
        pillTone === "live" || charging
          ? "border-emerald-500/40 text-emerald-300"
          : pillTone === "warn"
            ? "border-amber-500/40 text-amber-300"
            : "border-neutral-700 text-neutral-300"
      }`}
    >
      <span
        aria-hidden
        className={`h-2 w-2 shrink-0 rounded-full ${charging ? "bg-emerald-500" : "bg-neutral-500"}`}
      />
      {soft && chip.includes(" · done by ") ? (
        // Under 431 px the chip drops the finish time (the card says it in its own line), so the Live button keeps the row; the iPhone does the same.
        // From 431 px it keeps the time whole: with "done by" while the row has the room (24 rem), without those words (still the whole time) below.
        <>
          <span className="min-w-0 @[22rem]:truncate min-[431px]:hidden">{chip.slice(0, chip.indexOf(" · done by "))}</span>
          <span className="min-w-0 @[22rem]:truncate max-[430px]:hidden @[24rem]:hidden">{chip.replace(" · done by ", " · ")}</span>
          <span className="hidden min-w-0 @[22rem]:truncate @[24rem]:inline max-[430px]:hidden!">{chip}</span>
        </>
      ) : (
        <span className={tidy ? (soft ? "min-w-0 @[22rem]:truncate" : "min-w-0 truncate") : undefined}>{chip}</span>
      )}
    </span>
  );
  if (tidy) {
    const parked = stateSlot != null && parkedSlot;
    // The parked slot showed the weather whatever the outside-temperature setting; elsewhere the setting rules.
    const weather = ambient && (parked || amb.enabled) ? ambient : null;
    const items = conditionItems({ weather, tempUnit: temperatureUnit, unit: distanceUnit, nowMs, cabin, asleepTodayS: view.kind === "asleep" || (view.kind === "offline" && view.sleeping) ? asleepTodayS : null, detail: parked });
    const infoBtn = (c: CondItem) =>
      c.info && (
        <button
          type="button"
          aria-label="About the outside temperature"
          aria-expanded={info}
          onClick={() => setInfo((o) => !o)}
          className="pointer-events-auto -my-4 -mr-4 ml-0 inline-flex p-4 text-neutral-500 hover:text-neutral-300"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
            <circle cx="12" cy="12" r="9" />
            <path d="M12 11v6M12 7.5v.5" strokeLinecap="round" />
          </svg>
        </button>
      );
    const picture =
      stateSlot ? null : carUrl && carUrl !== failedUrl ? (
        plugPicture && (view.kind === "plugged" || view.kind === "charging") ? (
          <PlugPicture kw={view.powerKw} className="w-full">
            <img data-testid="now-car" src={carUrl} alt="" loading="lazy" onError={() => setFailedUrl(carUrl)} className="max-h-32 w-full object-contain object-left min-[900px]:max-h-60 min-[900px]:object-center" />
          </PlugPicture>
        ) : (
          <img data-testid="now-car" src={carUrl} alt="" loading="lazy" onError={() => setFailedUrl(carUrl)} className="max-h-32 w-full object-contain object-left min-[900px]:max-h-60 min-[900px]:object-center" />
        )
      ) : (
        <CarSketch model={model} charging={charging} className="min-[900px]:mx-auto min-[900px]:max-w-[440px]" />
      );
    if (soft) {
      // The charging and the driving card share one layout: ring, then the figures under it on the left, the graph on the right.
      const rail = charging || view.kind === "driving";
      const condBlock = items.length > 0 && (
        <div data-testid="now-temps" className="pointer-events-none relative flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1 text-sm">
          {[...items].sort((a, b) => Number(b.k === "Cabin") - Number(a.k === "Cabin")).map((c) => (
            <p key={c.k} data-testid={`now-cond-${c.k.split(" ")[0].toLowerCase()}`} className="min-w-0 text-neutral-400">
              {c.k}{" "}
              <span className="font-semibold tabular-nums text-neutral-100">{c.v}</span>
              {c.note && <span className="ml-1.5 text-neutral-400">{c.note}</span>}
              {infoBtn(c)}
            </p>
          ))}
        </div>
      );
      return (
        <div
          data-now-v2="true"
          data-now-tidy="true"
          data-now-soft="true"
          data-ui-hero="true"
          data-live={charging ? "charging" : undefined}
          className={`relative rounded-2xl border bg-neutral-900/60 p-5 ${critical ? "border-rose-500/50" : charging ? "border-emerald-500/30" : "border-neutral-800"}`}
        >
          {telemetryLink && <Link to="/live" aria-label="Full telemetry" className="absolute inset-0 z-0 rounded-2xl" />}
          <div className="@container pointer-events-none relative z-10 mb-4">
            <div className="flex flex-wrap @[22rem]:flex-nowrap items-center justify-between gap-3 max-[359px]:gap-2 [&_[data-now-chip]]:min-w-0 max-[359px]:[&_[data-now-chip]]:px-2.5 max-[359px]:[&_[data-now-chip]]:text-xs max-[430px]:[&_[data-now-chip]]:px-2.5 max-[430px]:[&_[data-now-chip]]:text-xs">
            {chipEl}
            {telemetryLink && (
              <Link
                to="/live"
                data-testid="now-live-link"
                className="pointer-events-auto inline-flex min-h-11 min-w-11 shrink-0 cursor-pointer items-center gap-2 rounded-full border border-neutral-600 bg-neutral-800 px-4 text-sm font-medium pointer-fine:min-h-8 pointer-fine:min-w-8 pointer-fine:gap-1.5 pointer-fine:px-3 pointer-fine:text-xs text-neutral-100 shadow-sm transition-colors hover:border-neutral-400 hover:bg-neutral-700 active:scale-95 active:bg-neutral-600"
              >
                {charging && <span aria-hidden className="h-2 w-2 animate-pulse rounded-full bg-emerald-500 motion-reduce:animate-none" />}
                Live
                <svg aria-hidden className="max-[430px]:hidden" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round">
                  <path d="m9 5 7 7-7 7" />
                </svg>
              </Link>
            )}
          </div>
          </div>
          <div className="grid grid-cols-[minmax(0,1fr)] gap-y-4 min-[900px]:grid-cols-[minmax(0,2fr)_minmax(0,3fr)] min-[900px]:gap-x-10">
            <div className="pointer-events-none relative flex min-w-0 flex-wrap items-center gap-x-5 gap-y-3 min-[900px]:col-start-1">
              <ArcGauge
                value={view.soc}
                label={view.soc > 0 ? `Battery ${Math.round(view.soc)} percent` : "Battery level unknown"}
                live={charging}
                tone={offline ? "idle" : critical ? "critical" : "ok"}
                limit={view.limitPct}
              >
                {(shown) => (
                  <span className={`font-semibold leading-none tabular-nums ${tone}`}>
                    <span className="text-5xl">{view.soc > 0 ? Math.round(shown) : "—"}</span>
                    <span className="ml-0.5 text-lg font-normal text-neutral-400">%</span>
                  </span>
                )}
              </ArcGauge>
              <div className="min-w-[min(10rem,100%)] flex-1 space-y-1">
                {view.rangeMi != null && (
                  <p>
                    <span className="whitespace-nowrap text-2xl font-semibold tabular-nums text-neutral-100 min-[360px]:text-3xl">{dist(view.rangeMi)}</span>{" "}
                    <span className="text-sm text-neutral-400">range</span>
                  </p>
                )}
                {subline && (
                  <p data-testid="now-subline" className="text-sm text-neutral-400 [text-wrap:balance]">
                    {subline}
                  </p>
                )}
              </div>
            </div>
            {!rail && (
              <div className="pointer-events-none relative min-w-0 border-t border-neutral-800 pt-4 min-[900px]:col-start-2 min-[900px]:row-span-3 min-[900px]:row-start-1 min-[900px]:border-l min-[900px]:border-t-0 min-[900px]:pl-10 min-[900px]:pt-0">{graph}</div>
            )}
            {!rail && parkedRows && <div className="pointer-events-none relative min-w-0 min-[900px]:col-start-1 min-[900px]:row-start-2">{parkedRows}</div>}
            <div className={`min-w-0 border-t border-neutral-800 pt-4 ${rail ? "min-[900px]:col-start-1" : "min-[900px]:col-span-2 min-[900px]:row-start-4"}`}>{stateSlot}</div>
            {rail && <div className="pointer-events-none relative min-w-0 border-t border-neutral-800 pt-4 min-[900px]:col-start-2 min-[900px]:row-span-3 min-[900px]:row-start-1 min-[900px]:border-l min-[900px]:border-t-0 min-[900px]:pl-10 min-[900px]:pt-0">{graph}</div>}
            {(condBlock || info) && (
              <div className={`min-w-0 space-y-2 border-t border-neutral-800 pt-4 min-[900px]:col-start-1 ${rail ? "" : "min-[900px]:row-start-3"}`}>
                {condBlock}
                {info && (
                  <p className="pointer-events-none relative text-xs text-neutral-400">
                    Outside is an estimate, looked up from Open-Meteo for your truck&apos;s location.
                  </p>
                )}
              </div>
            )}
          </div>
          <div className="pointer-events-none relative [&_a]:pointer-events-auto [&_button]:pointer-events-auto">{extra}</div>
          {footer}
        </div>
      );
    }
    return (
      <div
        data-now-v2="true"
        data-now-tidy="true"
        data-ui-hero="true"
        data-live={charging ? "charging" : view.kind === "driving" ? "driving" : undefined}
        className={`relative rounded-2xl border bg-neutral-900/60 p-5 ${critical ? "border-rose-500/50" : charging ? "border-emerald-500/30" : "border-neutral-800"}`}
      >
        {telemetryLink && <Link to="/live" aria-label="Full telemetry" className="absolute inset-0 z-0 rounded-2xl" />}
        {/* The one status line: the state and since when, with the Live button on the same row. */}
        <div className="pointer-events-none relative z-10 mb-4 flex items-center justify-between gap-3 max-[359px]:gap-2 [&_[data-now-chip]]:min-w-0 max-[359px]:[&_[data-now-chip]]:px-2.5 max-[359px]:[&_[data-now-chip]]:text-xs">
          {chipEl}
          {telemetryLink && (
            <Link
              to="/live"
              data-testid="now-live-link"
              className="pointer-events-auto inline-flex min-h-11 min-w-11 shrink-0 cursor-pointer items-center gap-2 rounded-full border border-neutral-600 bg-neutral-800 px-4 text-sm font-medium pointer-fine:min-h-8 pointer-fine:min-w-8 pointer-fine:gap-1.5 pointer-fine:px-3 pointer-fine:text-xs text-neutral-100 shadow-sm transition-colors hover:border-neutral-400 hover:bg-neutral-700 active:scale-95 active:bg-neutral-600"
            >
              {(charging || view.kind === "driving") && <span aria-hidden className="h-2 w-2 animate-pulse rounded-full bg-emerald-500 motion-reduce:animate-none" />}
              Live
              <svg aria-hidden className="max-[359px]:hidden" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round">
                <path d="m9 5 7 7-7 7" />
              </svg>
            </Link>
          )}
        </div>
        <div className="min-[900px]:grid min-[900px]:grid-cols-[auto_minmax(0,1fr)] min-[900px]:items-center min-[900px]:gap-x-12">
          {/* Battery and range are the hero. */}
          <div className="pointer-events-none relative flex items-center gap-5 min-[900px]:flex-col min-[900px]:items-center min-[900px]:gap-3">
            <ArcGauge
              value={view.soc}
              label={view.soc > 0 ? `Battery ${Math.round(view.soc)} percent` : "Battery level unknown"}
              live={charging}
              tone={offline ? "idle" : critical ? "critical" : "ok"}
              limit={charging || view.kind === "plugged" ? view.limitPct : null}
            >
              {(shown) => (
                <span className={`font-semibold leading-none tabular-nums ${tone}`}>
                  <span className="text-5xl">{view.soc > 0 ? Math.round(shown) : "—"}</span>
                  <span className="ml-0.5 text-lg font-normal text-neutral-400">%</span>
                </span>
              )}
            </ArcGauge>
            <div className="min-w-0 min-[900px]:text-center">
              {view.rangeMi != null && (
                <p>
                  <span className="whitespace-nowrap text-2xl font-semibold min-[360px]:text-3xl tabular-nums text-neutral-100">{dist(view.rangeMi)}</span>{" "}
                  <span className="text-sm text-neutral-400">range</span>
                </p>
              )}
              {subline && (
                <p data-testid="now-subline" className="mt-1 text-sm text-neutral-400">
                  {subline}
                  {fullCharge != null && !charging && <span className="sr-only"> (estimate from your own drives)</span>}
                </p>
              )}
            </div>
          </div>
          <div className="mt-4 min-w-0 space-y-4 border-t border-neutral-800 pt-4 min-[900px]:mt-0 min-[900px]:border-t-0 min-[900px]:pt-0 empty:hidden">
            {stateSlot}
            {picture}
            {rows.length > 0 && (
              <dl className="pointer-events-none relative grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
                {rows.map((d) => (
                  <div key={d.k} className="contents">
                    <dt className="text-neutral-500">{d.k}</dt>
                    <dd className="text-right tabular-nums text-neutral-200">{d.v}</dd>
                  </div>
                ))}
              </dl>
            )}
            {items.length > 0 && (
              <div className="pointer-events-none relative space-y-3">
                <div data-testid="now-temps" className="grid grid-cols-2 gap-x-4 gap-y-3 min-[480px]:grid-cols-[repeat(auto-fit,minmax(6.5rem,1fr))]">
                  {items.slice(0, items.length - (items.length % 2)).map((c) => (
                    <div key={c.k} data-testid={`now-cond-${c.k.split(" ")[0].toLowerCase()}`} className="min-w-0">
                      <p className="ui-label flex items-center">
                        {c.k}
                        {infoBtn(c)}
                      </p>
                      <p className="text-lg font-semibold tabular-nums text-neutral-100">
                        {c.v}
                        {c.note && <span className="ml-1.5 text-sm font-normal text-neutral-400">{c.note}</span>}
                      </p>
                    </div>
                  ))}
                </div>
                {items.length % 2 === 1 && (
                  // An odd last item is a footer line, so the grid above always ends evenly.
                  <div
                    data-testid={`now-cond-${items[items.length - 1].k.split(" ")[0].toLowerCase()}`}
                    className="flex items-baseline justify-between gap-3 border-t border-neutral-800 pt-3"
                  >
                    <p className="ui-label flex items-center">
                      {items[items.length - 1].k}
                      {infoBtn(items[items.length - 1])}
                    </p>
                    <p className="text-lg font-semibold tabular-nums text-neutral-100">
                      {items[items.length - 1].v}
                      {items[items.length - 1].note && (
                        <span className="ml-1.5 text-sm font-normal text-neutral-400">{items[items.length - 1].note}</span>
                      )}
                    </p>
                  </div>
                )}
              </div>
            )}
            {info && (
              <p className="pointer-events-none relative text-xs text-neutral-400">
                Outside is an estimate, looked up from Open-Meteo for your truck&apos;s location.
              </p>
            )}
          </div>
        </div>
        <div className="pointer-events-none relative [&_a]:pointer-events-auto [&_button]:pointer-events-auto">{extra}</div>
        {footer}
      </div>
    );
  }
  return (
    <>
      {slot ? (
        createPortal(chipEl, slot)
      ) : (
        <div className="flex justify-end">{chipEl}</div>
      )}
      <div
        data-now-v2="true"
        data-ui-hero="true"
        data-live={
          charging
            ? "charging"
            : view.kind === "driving"
              ? "driving"
              : undefined
        }
        className={`relative rounded-2xl border bg-neutral-900/60 p-5 ${critical ? "border-rose-500/50" : charging ? "border-emerald-500/30" : "border-neutral-800"}`}
      >
        {telemetryLink && (
          <>
            {/* The whole card opens Full telemetry; controls inside sit above this link. */}
            <Link
              to="/live"
              aria-label="Full telemetry"
              className="absolute inset-0 z-0 rounded-2xl"
            />
            {/* The pill has its own row above the gauge and the truck, so nothing overlaps it. */}
            <div className="pointer-events-none relative z-10 mb-2 flex justify-end">
            <Link
              to="/live"
              data-testid="now-live-link"
              className="pointer-events-auto inline-flex min-h-11 min-w-11 cursor-pointer items-center gap-2 rounded-full border border-neutral-600 bg-neutral-800 px-4 text-sm font-medium pointer-fine:min-h-8 pointer-fine:min-w-8 pointer-fine:gap-1.5 pointer-fine:px-3 pointer-fine:text-xs text-neutral-100 shadow-sm transition-colors hover:border-neutral-400 hover:bg-neutral-700 active:scale-95 active:bg-neutral-600"
            >
              {(charging || view.kind === "driving") && (
                <span aria-hidden className="h-2 w-2 animate-pulse rounded-full bg-emerald-500 motion-reduce:animate-none" />
              )}
              Live
              <svg aria-hidden width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round">
                <path d="m9 5 7 7-7 7" />
              </svg>
            </Link>
            </div>
          </>
        )}
        {/* From 900 px up the card fills the width: the gauge with the range under it, the truck large beside them, and the context in a column on the right. */}
        <div className="min-[900px]:grid min-[900px]:grid-cols-[auto_minmax(0,1fr)_17rem] min-[900px]:items-center min-[900px]:gap-x-10 min-[900px]:gap-y-2">
        <div className="pointer-events-none relative flex items-center gap-4 min-[900px]:contents">
          <ArcGauge
            value={view.soc}
            label={
              view.soc > 0
                ? `Battery ${Math.round(view.soc)} percent`
                : "Battery level unknown"
            }
            live={charging}
            tone={offline ? "idle" : critical ? "critical" : "ok"}
            limit={charging || view.kind === "plugged" ? view.limitPct : null}
          >
            {(shown) => (
              <span
                className={`font-semibold leading-none tabular-nums ${tone}`}
              >
                <span className="text-5xl">
                  {view.soc > 0 ? Math.round(shown) : "—"}
                </span>
                <span className="ml-0.5 text-lg font-normal text-neutral-400">
                  %
                </span>
              </span>
            )}
          </ArcGauge>
          <div className="flex min-w-0 flex-1 flex-col items-start gap-1 min-[900px]:contents">
            {stateSlot ? null : carUrl && carUrl !== failedUrl ? (
              plugPicture && (view.kind === "plugged" || view.kind === "charging") ? (
                <PlugPicture kw={view.powerKw} className="w-full min-[900px]:col-start-2 min-[900px]:row-span-2 min-[900px]:row-start-1">
                  <img data-testid="now-car" src={carUrl} alt="" loading="lazy" onError={() => setFailedUrl(carUrl)} className="max-h-24 w-full object-contain object-left min-[900px]:max-h-60 min-[900px]:object-center" />
                </PlugPicture>
              ) : (
                <img data-testid="now-car" src={carUrl} alt="" loading="lazy" onError={() => setFailedUrl(carUrl)} className="max-h-24 w-full object-contain object-left min-[900px]:col-start-2 min-[900px]:row-span-2 min-[900px]:row-start-1 min-[900px]:max-h-60 min-[900px]:object-center" />
              )
            ) : (
              <CarSketch model={model} charging={charging} className="min-[900px]:col-start-2 min-[900px]:row-span-2 min-[900px]:row-start-1 min-[900px]:mx-auto min-[900px]:max-w-[440px]" />
            )}
            {view.rangeMi != null && (
              <p className="min-w-0 min-[900px]:col-start-1 min-[900px]:row-start-2 min-[900px]:text-center">
                <span className="text-2xl font-semibold tabular-nums text-neutral-100">
                  {dist(view.rangeMi)}
                </span>{" "}
                <span className="text-sm text-neutral-400">range</span>
              </p>
            )}
          </div>
        </div>
        {stateSlot}
        <div className="min-[900px]:col-start-3 min-[900px]:row-span-2 min-[900px]:row-start-1 min-[900px]:border-l min-[900px]:border-neutral-800 min-[900px]:pl-6">
        {subline && (
          <p
            data-testid="now-subline"
            className="mt-3 text-sm text-neutral-400 min-[900px]:mt-0"
          >
            {subline}
            {fullCharge != null && !charging && (
              <span className="sr-only"> (estimate from your own drives)</span>
            )}
          </p>
        )}
        {rows.length > 0 && (
          <dl className="mt-3 min-[900px]:first:mt-0 grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
            {rows.map((d) => (
              <div key={d.k} className="contents">
                <dt className="text-neutral-500">{d.k}</dt>
                <dd className="text-right tabular-nums text-neutral-200">
                  {d.v}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {(cabin || outside) && (
          <div
            // On a phone the temps sit under the truck and the rule separates them. In the desktop column, when nothing
            // comes before them (parked, no subline or rows), the rule would hang over empty space, so it goes.
            className={`pointer-events-none relative mt-3 border-t border-neutral-800 pt-3 text-sm text-neutral-300${
              !subline && rows.length === 0 ? " min-[900px]:mt-0 min-[900px]:border-t-0 min-[900px]:pt-0" : ""
            }`}
            data-testid="now-temps"
          >
            <span>
              {[cabin && `Cabin ${cabin}`, outside && `Outside ${outside}`]
                .filter(Boolean)
                .join(" · ")}
            </span>
            {outside && (
              <button
                type="button"
                aria-label="About the outside temperature"
                aria-expanded={info}
                onClick={() => setInfo((o) => !o)}
                className="pointer-events-auto -my-3.5 -mr-3.5 ml-0 inline-flex p-3.5 align-middle text-neutral-500 hover:text-neutral-300"
              >
                <svg
                  width="18"
                  height="18"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.8"
                >
                  <circle cx="12" cy="12" r="9" />
                  <path d="M12 11v6M12 7.5v.5" strokeLinecap="round" />
                </svg>
              </button>
            )}
            {outside && info && (
              <p className="mt-2 text-xs text-neutral-400">
                Outside is an estimate, looked up from Open-Meteo for your
                truck&apos;s location.
              </p>
            )}
          </div>
        )}
        </div>
        </div>
        <div className="pointer-events-none relative [&_a]:pointer-events-auto [&_button]:pointer-events-auto">
          {extra}
        </div>
        {footer}
      </div>
    </>
  );
}
