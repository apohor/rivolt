import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router-dom";
import { ArcGauge } from "./ArcGauge";
import { useColdRange } from "./AmbientTempLine";
import { pickImageForNow, useIsLight, type PlacedImage } from "../lib/vehicleImage";
import { useAmbientTemp } from "../lib/ambientTemp";
import { formatTemperature, usePreferences } from "../lib/preferences";
import { clock, type NowView } from "../lib/nowState";

// The redesigned Now hero (design_preview "now_v2"): a status chip, then one card with the gauge, the range,
// one line of context and the temperatures. Same data as the card it replaces; only the layout is new.

export function nowChip(
  v: NowView,
  asleepSinceMs: number | null,
  nowMs: number,
  pillText: string,
): string {
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
}: {
  view: NowView;
  vehicleID: string;
  chip: string;
  pillTone: "live" | "warn" | "neutral";
  ambient?: { temp_c: number } | null;
  cabin: string | null;
  tone: string;
  rows: { k: string; v: string }[];
  extra: React.ReactNode;
  footer: React.ReactNode;
  telemetryLink: boolean;
  critical: boolean;
  // The vehicle's Rivian images; without any the drawn truck below stays (the line-art fallback slots in there).
  images?: PlacedImage[];
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
  const subline = charging
    ? view.limitPct != null
      ? `Charging to ${Math.round(view.limitPct)}%${atLimit != null ? ` · ≈ ${dist(atLimit)} when done` : ""}`
      : null
    : fullCharge != null
      ? `Full charge today ≈ ${fullCharge} ${distanceUnit}`
      : null;
  const outside =
    amb.enabled && ambient
      ? formatTemperature(ambient.temp_c, temperatureUnit)
      : null;
  // The chip sits in the page header's title row (Now ... chip), as in the design; inline when the page has no slot.
  const carUrl = pickImageForNow(images, charging ? "charging" : view.kind, light);
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
        className={`h-2 w-2 rounded-full ${charging ? "bg-emerald-500" : "bg-neutral-500"}`}
      />
      {chip}
    </span>
  );
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
              className="pointer-events-auto inline-flex min-h-11 min-w-11 cursor-pointer items-center gap-2 rounded-full border border-neutral-600 bg-neutral-800 px-4 text-sm font-medium text-neutral-100 shadow-sm transition-colors hover:border-neutral-400 hover:bg-neutral-700 active:scale-95 active:bg-neutral-600"
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
        <div className="pointer-events-none relative flex items-center gap-4">
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
          <div className="flex min-w-0 flex-1 flex-col items-start gap-1">
            {carUrl && carUrl !== failedUrl ? (
              <img data-testid="now-car" src={carUrl} alt="" loading="lazy" onError={() => setFailedUrl(carUrl)} className="max-h-24 w-full object-contain object-left" />
            ) : (
              <Truck charging={charging} />
            )}
            {view.rangeMi != null && (
              <p className="min-w-0">
                <span className="text-2xl font-semibold tabular-nums text-neutral-100">
                  {dist(view.rangeMi)}
                </span>{" "}
                <span className="text-sm text-neutral-400">range</span>
              </p>
            )}
          </div>
        </div>
        {subline && (
          <p
            data-testid="now-subline"
            className="mt-3 text-sm text-neutral-400"
          >
            {subline}
            {fullCharge != null && !charging && (
              <span className="sr-only"> (estimate from your own drives)</span>
            )}
          </p>
        )}
        {rows.length > 0 && (
          <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
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
            className="pointer-events-none relative mt-3 border-t border-neutral-800 pt-3 text-sm text-neutral-300"
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
        <div className="pointer-events-none relative [&_a]:pointer-events-auto [&_button]:pointer-events-auto">
          {extra}
        </div>
        {footer}
      </div>
    </>
  );
}

// A plain pickup in side view: no badges, lights or grille. Colours follow the theme through the neutral scale.
function Truck({ charging }: { charging: boolean }) {
  return (
    <svg
      viewBox="0 0 240 104"
      role="img"
      aria-label="Truck"
      className="h-auto w-full max-w-[200px]"
    >
      <ellipse cx="120" cy="91" rx="112" ry="6" className="fill-black/30" />
      <path
        d="M8 76 V58 Q8 50 16 49 L54 45 L80 26 Q83 24 88 24 L140 24 Q146 24 148 29 L154 45 L226 45 Q233 45 233 52 L233 76 L210 76 A21 21 0 0 0 168 76 L72 76 A21 21 0 0 0 30 76 Z"
        className="fill-neutral-700"
      />
      <path
        d="M63 44 L82 30 Q84 28.5 87 28.5 L110 28.5 L110 44 Z"
        className="fill-neutral-500"
      />
      <path
        d="M114 28.5 L139 28.5 Q142.5 28.5 143.5 31.5 L148.5 44 L114 44 Z"
        className="fill-neutral-500"
      />
      <path
        d="M112 46 V74 M156 47 V74"
        className="stroke-neutral-800"
        strokeWidth="1.2"
        fill="none"
      />
      {[51, 189].map((cx) => (
        <g key={cx}>
          <circle cx={cx} cy="78" r="16" className="fill-neutral-900" />
          <circle
            cx={cx}
            cy="78"
            r="9"
            className="fill-none stroke-neutral-500"
            strokeWidth="3"
          />
        </g>
      ))}
      {charging && (
        <g transform="translate(206 30)">
          <circle r="11" className="fill-emerald-500" />
          <path d="M1.5 -6.5 -4 1h4l-1.5 5.5 5.5-7.5h-4z" fill="#06080C" />
        </g>
      )}
    </svg>
  );
}
