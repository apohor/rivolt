import { UNSET_RATE_NOTE, memberSaving, networkRateSet } from "../lib/networkRates";
import { useAIEnabled } from "../lib/config";
import { useAIConsent } from "../lib/aiConsent";
import { AMBIENT_TEMP_TEXT, useAmbientTemp } from "../lib/ambientTemp";
import { R2CheckedSettings } from "../components/R2Checked";
import { PlugReminderFields, useEngagement } from "../components/AlertSettings";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { inDemo } from "../lib/demo";
import DemoSettings from "./DemoSettings";
import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import {
  ApiError,
  backend,
  type AIProvider,
  type AISettings,
  type AISettingsUpdate,
  type AIPingResult,
  type ChargingNetwork,
  type DriveWeatherBackfillResult,
  type GeocodeResult,
  type HomeLocation,
  type RestoreProgress,
  type PlannerPrefs,
  type ImportResult,
  type ImportProgress,
  type RecapSettingsUpdate,
  type GPSSettingsUpdate,
  type PushPreferences,
} from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner, Toggle } from "../components/ui";
import { SignedInDevices } from "../components/SignedInDevices";
import { PasskeysCard } from "../components/PasskeysCard";
import { AccountSecurityCards } from "../components/AccountSecurityCards";
import { restorePartialNote, restoreSoFar } from "../lib/restoreProgress";
import { VehicleProfilePanel } from "../components/VehicleProfilePanel";
import { LeasePanel } from "../components/LeasePanel";
import { OffpeakPanel } from "../components/OffpeakCard";
import { MembershipTerms } from "../components/MembershipTerms";
import { IosAppCard } from "../components/IosAppCard";
import { useHomeRecheck } from "../components/HomeRecheck";
import { formatNetworkLocation, parseNetworkLocation } from "../lib/networkLocation";
import { registerPushSubscription } from "../lib/push";
import { RivianAccountPanel } from "../components/RivianAccountPanel";
import { SignOutButton } from "../components/SignOutButton";
import { track } from "../lib/track";
import { useThemeChoice, type ThemeChoice } from "../lib/theme";
import { useImpersonationTarget } from "../lib/impersonation";
import {
  setTemperatureUnit,
  setDistanceUnit,
  type DistanceUnit,
  setTimeZone,
  setRoundTripsEnabled,
  setRoundTripRadiusMeters,
  setRoundTripMaxGapMinutes,
  usePreferences,
  type TemperatureUnit,
} from "../lib/preferences";

type TabId = "account" | "security" | "vehicle" | "charging" | "notifications" | "data";

// Which tab "owns" a given section anchor id — used so a deep link
// like /settings#rivian still auto-selects the right tab on mount.
// Input that means the reader is scrolling on their own, so stop re-aligning.
const USER_SCROLL_EVENTS = ["wheel", "touchstart", "keydown", "pointerdown"] as const;

// Every card id a link may point at; a missing one opens Account with no card.
const HASH_TO_TAB: Record<string, TabId> = {
  rivian: "account",
  "ai-features": "account",
  "ambient-temp": "account",
  security: "security",
  passkeys: "security",
  devices: "security",
  password: "security",
  display: "vehicle",
  home: "vehicle",
  profile: "vehicle",
  lease: "vehicle",
  cost: "charging",
  offpeak: "charging",
  networks: "charging",
  planner: "charging",
  notifications: "notifications",
  "statement-email": "notifications",
  "weekly-recap": "notifications",
  "onboarding-email": "notifications",
  export: "data",
  import: "data",
  danger: "data",
};

// How far down the sticky bars reach once stuck: the top nav and, in the new look, the page's own title row
// ([data-ui-header]). An anchored card lands just below them; a fixed scroll margin was too short on a phone.
function stickyTopOffset(): number {
  let bottom = 0;
  document.querySelectorAll<HTMLElement>("header, [data-ui-header]").forEach((el) => {
    const cs = getComputedStyle(el);
    if (cs.position !== "sticky" && cs.position !== "fixed") return;
    const top = parseFloat(cs.top);
    if (Number.isNaN(top)) return;
    bottom = Math.max(bottom, top + el.offsetHeight);
  });
  return bottom;
}

const TABS: { id: TabId; label: string }[] = [
  { id: "account", label: "Account" },
  { id: "security", label: "Security" },
  { id: "vehicle", label: "Vehicle" },
  { id: "charging", label: "Charging" },
  { id: "notifications", label: "Notifications" },
  { id: "data", label: "Data" },
];

function SettingsPageInner() {
  const location = useLocation();
  // The tab's cards that load their own data appear after the first frame; watching this root (not the target's
  // parent, which does not exist yet) is what lets the anchor re-align once the target card has mounted.
  const rootRef = useRef<HTMLDivElement>(null);

  // Tab state. Priority: ?tab=… query param > hash → owning tab >
  // default "account". Query param wins so a bookmark like
  // /settings?tab=charging always lands on the same tab.
  const initialTab: TabId = (() => {
    const sp = new URLSearchParams(location.search);
    const t = sp.get("tab") as TabId | null;
    if (t && TABS.some((x) => x.id === t)) return t;
    if (location.hash) {
      const owner = HASH_TO_TAB[location.hash.slice(1)];
      if (owner) return owner;
    }
    return "account";
  })();
  const [tab, setTab] = useState<TabId>(initialTab);

  // Hash scroll inside the active tab. Same rAF dance as before;
  // the target card only exists in the DOM once its owning tab is
  // selected, so flip the tab first then scroll on the next frame.
  useEffect(() => {
    if (!location.hash) return;
    const id = location.hash.slice(1);
    const owner = HASH_TO_TAB[id];
    if (owner && owner !== tab) {
      setTab(owner);
      return; // next render will re-run this effect (tab is in deps)
    }
    // Cards above the target (the vehicle profile, the lease terms) fill in after the first frame
    // and push it down, so keep it pinned to the top until the layout stops moving, the user takes
    // over the scroll, or a few seconds pass.
    const align = (behavior: ScrollBehavior) => {
      const el = document.getElementById(id);
      if (!el) return;
      const top = el.getBoundingClientRect().top + window.scrollY - stickyTopOffset() - 8;
      window.scrollTo({ top: Math.max(0, top), behavior });
    };
    const raf = requestAnimationFrame(() => align("smooth"));
    let ro: ResizeObserver | undefined;
    const stop = () => {
      ro?.disconnect();
      for (const ev of USER_SCROLL_EVENTS) window.removeEventListener(ev, stop);
    };
    if (typeof ResizeObserver !== "undefined") {
      ro = new ResizeObserver(() => align("auto"));
      const root = rootRef.current ?? document.getElementById(id)?.parentElement;
      if (root) ro.observe(root);
    }
    for (const ev of USER_SCROLL_EVENTS) window.addEventListener(ev, stop, { passive: true });
    const timer = window.setTimeout(stop, 4000);
    return () => {
      cancelAnimationFrame(raf);
      clearTimeout(timer);
      stop();
    };
  }, [location.hash, tab]);

  // Reflect tab into ?tab= so reloads / back nav land on the same
  // pane. replace, not push, so each click doesn't fill history.
  useEffect(() => {
    const sp = new URLSearchParams(window.location.search);
    if (sp.get("tab") === tab) return;
    sp.set("tab", tab);
    const next = `${window.location.pathname}?${sp.toString()}${window.location.hash}`;
    window.history.replaceState(null, "", next);
  }, [tab]);

  return (
    <div ref={rootRef} className="space-y-4">
      <PageHeader title="Settings" />

      <div className="flex flex-wrap gap-1 border-b border-neutral-800 overflow-x-auto">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={`px-3 py-2 text-sm transition-colors ${
              tab === t.id
                ? "border-b-2 border-emerald-500 text-neutral-100"
                : "border-b-2 border-transparent text-neutral-400 hover:text-neutral-100"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "account" && (
        <>
          <Card title="Rivian account" id="rivian">
            <RivianAccountPanel />
          </Card>
          <AIFeaturesCard />
          <AmbientTempCard />
          <AppearanceCard />
          <IosAppCard />
          {/* The tab-bar shell's header has no Sign out; it lives here. */}
          <Card title="Rivolt account">
            <SignOutButton label="Sign out of Rivolt" solid />
          </Card>
        </>
      )}

      {tab === "security" && (
        <>
          <AccountSecurityCards />
          <PasskeysCard />
          <SignedInDevices />
        </>
      )}

      {tab === "vehicle" && (
        <>
          <Card title="Vehicle profile" id="profile">
            <VehicleProfilePanel />
          </Card>
          <R2CheckedSettings />
          <Card title="Lease" id="lease">
            <LeasePanel />
          </Card>
          <Card title="Display" id="display">
            <DisplayPreferences />
          </Card>
          <Card title="Home location" id="home">
            <HomeLocationPanel />
          </Card>
        </>
      )}

      {tab === "charging" && (
        <>
          <Card title="Home charging cost" id="cost">
            <ChargingCostPanel />
          </Card>
          <Card title="Off-peak schedule" id="offpeak">
            <OffpeakPanel />
          </Card>
          <Card title="Charging networks" id="networks">
            <ChargingNetworksPanel />
            <MembershipTerms />
          </Card>
          <Card title="Trip planner defaults" id="planner">
            <PlannerPrefsPanel />
          </Card>
        </>
      )}

      {tab === "notifications" && (
        <>
          <Card title="Notifications" id="notifications">
            <NotificationsPanel />
          </Card>
          <StatementEmailCard />
          <WeeklyRecapCard />
          <TripRecapCard />
          <OnboardingEmailCard />
        </>
      )}

      {tab === "data" && (
        <>
          <ExportCard />
          <Card title="Import ElectraFi CSV" id="import">
            <ImportPanel />
          </Card>
          <Card title="Danger zone" id="danger">
            <DangerZonePanel />
          </Card>
        </>
      )}
    </div>
  );
}

// previewReason: why last month's email would not be sent, in plain words.
function previewReason(reason?: string): string {
  switch (reason) {
    case "not_opted_in":
      return "This would not be sent: monthly statements are off for your account.";
    case "no_email":
      return "This would not be sent: your account has no email address.";
    case "silent_month":
      return "This would not be sent: there was no charging or driving that month.";
    default:
      return "This would not be sent.";
  }
}

function browserZone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
  } catch {
    return undefined;
  }
}

// OnboardingEmailCard: the welcome email and up to two setup reminders (until the truck is
// connected). On by default; separate from the monthly statement. Hidden when the endpoint is absent.
function OnboardingEmailCard() {
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["settings", "onboarding-email"],
    queryFn: () => backend.onboardingEmailGet(),
    retry: 0,
    staleTime: 60_000,
  });
  const put = useMutation({
    mutationFn: (enabled: boolean) => backend.onboardingEmailPut(enabled),
    onSuccess: (data) => qc.setQueryData(["settings", "onboarding-email"], data),
  });
  if (!settings.data) return null;
  return (
    <Card title="Setup emails" id="onboarding-email">
      <div className="flex items-start justify-between gap-4">
        <div>
          <label htmlFor="onboarding-email-toggle" className="text-sm text-neutral-100">
            Welcome and setup reminder emails
          </label>
          <p className="mt-1 text-xs text-neutral-500">
            We send a welcome email and up to two setup reminders until your truck is connected. Turn them off here
            or with the link in the email.
          </p>
        </div>
        <Toggle
          id="onboarding-email-toggle"
          checked={settings.data.enabled}
          disabled={put.isPending}
          onChange={(v) => put.mutate(v)}
        />
      </div>
      {put.isError && (
        <p role="alert" className="mt-2 text-xs text-rose-400">
          Couldn&apos;t save: {String(put.error)}
        </p>
      )}
    </Card>
  );
}

// StatementEmailCard: opt in to the monthly statement email, and preview
// what last month's would look like (never sent). Off by default; hidden
// when the server says it isn't available (flag off, or no email on the
// account) or the endpoint is absent.
export function StatementEmailCard() {
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["settings", "statement-email"],
    queryFn: () => backend.statementEmailGet(),
    retry: 0,
    staleTime: 60_000,
  });
  const put = useMutation({
    // The browser's zone is sent when the user turns it on, so the 1st
    // of the month is theirs and not the server's.
    mutationFn: (enabled: boolean) => backend.statementEmailPut(enabled, browserZone()),
    onSuccess: (data, enabled) => {
      qc.setQueryData(["settings", "statement-email"], data);
      track(enabled ? "statement_email_optin" : "statement_email_optout", { from: "settings" });
    },
  });
  const [showPreview, setShowPreview] = useState(false);
  const [mode, setMode] = useState<"text" | "html">("text");
  const preview = useQuery({
    queryKey: ["statement-email", "preview"],
    queryFn: () => backend.statementEmailPreview(),
    enabled: showPreview,
    retry: 0,
  });
  const s = settings.data;
  if (!s || !s.available) return null;
  return (
    <Card title="Monthly statement" id="statement-email">
      <div className="flex items-start justify-between gap-4">
        <div>
          <label htmlFor="statement-email-toggle" className="text-sm text-neutral-100">
            Monthly statement by email
          </label>
          <p className="mt-1 text-xs text-neutral-500">
            On the 1st of each month we&apos;ll email last month&apos;s cost, split and biggest charges. Off by
            default; one click to stop.
          </p>
        </div>
        <Toggle
          id="statement-email-toggle"
          checked={s.enabled}
          disabled={put.isPending}
          onChange={(v) => put.mutate(v)}
        />
      </div>
      {put.isError && (
        <p role="alert" className="mt-2 text-xs text-rose-400">
          {put.error instanceof ApiError && put.error.status === 409
            ? "Add an email address to your account to receive the statement."
            : `Couldn't save: ${String(put.error)}`}
        </p>
      )}
      <div className="mt-4">
        <button
          type="button"
          aria-expanded={showPreview}
          onClick={() => setShowPreview((v) => !v)}
          className="text-xs text-emerald-300 hover:underline"
        >
          {showPreview ? "▾" : "▸"} Preview last month&apos;s email
        </button>
        {showPreview && (
          <div className="mt-2 space-y-2">
            {preview.isLoading && <Spinner />}
            {preview.isError && <p className="text-xs text-neutral-500">No preview available right now.</p>}
            {preview.data && (
              <>
                <p className="text-xs text-neutral-400">
                  <span className="text-neutral-500">Subject:</span> {preview.data.subject}
                </p>
                {!preview.data.will_send && (
                  <p className="text-xs text-amber-300">{previewReason(preview.data.reason)}</p>
                )}
                <div role="tablist" aria-label="Preview format" className="inline-flex rounded-lg border border-neutral-800 p-0.5 text-xs">
                  {(["text", "html"] as const).map((k) => (
                    <button
                      key={k}
                      type="button"
                      role="tab"
                      aria-selected={mode === k}
                      onClick={() => setMode(k)}
                      className={`rounded-md px-2.5 py-1 ${mode === k ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
                    >
                      {k === "text" ? "Text" : "HTML"}
                    </button>
                  ))}
                </div>
                {mode === "text" ? (
                  <pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded-md border border-neutral-800 bg-neutral-950 p-3 text-xs text-neutral-300">
                    {preview.data.text}
                  </pre>
                ) : (
                  // Sandboxed with no permissions: it's our own template,
                  // but a preview must never run anything.
                  <iframe
                    title="Email preview"
                    sandbox=""
                    srcDoc={preview.data.html}
                    className="h-96 w-full rounded-md border border-neutral-800 bg-white"
                  />
                )}
              </>
            )}
          </div>
        )}
      </div>
    </Card>
  );
}

// ExportCard: a plain download link to GET /api/export (a zip of CSVs
// and settings; same-origin cookie auth, attachment response). Hidden
// while an admin is viewing as someone else: a plain navigation doesn't
// carry the impersonation header, so it would export the admin's own
// data under the wrong person's name.
export function ExportCard() {
  const impersonating = useImpersonationTarget() != null;
  if (impersonating) return null;
  return (
    <Card title="Export my data" id="export">
      <p className="mb-3 text-xs text-neutral-500">
        A zip of CSV files (charges with cost source, drives, trips) and your settings. Raw telemetry is in Backup
        below.
      </p>
      <a
        href="/api/export"
        download
        className="inline-block rounded-md border border-neutral-700 bg-neutral-800 px-3 py-1.5 text-xs text-neutral-200 hover:bg-neutral-700"
      >
        Export my data
      </a>
    </Card>
  );
}

// DisplayPreferences surfaces the client-side display toggles
// (units, etc.) backed by localStorage via usePreferences().
export function DisplayPreferences() {
  const {
    temperatureUnit,
    distanceUnit,
    timeZone,
    roundTripsEnabled,
    roundTripRadiusMeters,
    roundTripMaxGapMinutes,
  } = usePreferences();
  const options: { value: TemperatureUnit; label: string }[] = [
    { value: "c", label: "Celsius (°C)" },
    { value: "f", label: "Fahrenheit (°F)" },
  ];
  const distanceOptions: { value: DistanceUnit; label: string }[] = [
    { value: "mi", label: "Miles (mi)" },
    { value: "km", label: "Kilometers (km)" },
  ];
  // Populate the time-zone select from the platform's IANA list when
  // available; fall back to a curated short list on older browsers
  // that don't expose Intl.supportedValuesOf.
  const zones: string[] =
    typeof (Intl as unknown as { supportedValuesOf?: (k: string) => string[] })
      .supportedValuesOf === "function"
      ? (Intl as unknown as { supportedValuesOf: (k: string) => string[] })
          .supportedValuesOf("timeZone")
      : [
          "UTC",
          "America/Los_Angeles",
          "America/Denver",
          "America/Chicago",
          "America/New_York",
          "Europe/London",
          "Europe/Berlin",
          "Asia/Tokyo",
        ];
  const browserZone =
    typeof Intl !== "undefined"
      ? Intl.DateTimeFormat().resolvedOptions().timeZone
      : "UTC";
  return (
    <div className="space-y-4 text-sm">
      <div>
        <div className="text-neutral-400 mb-1">Temperature</div>
        <div className="inline-flex rounded-md border border-neutral-700 overflow-hidden">
          {options.map((opt) => {
            const active = opt.value === temperatureUnit;
            return (
              <button
                key={opt.value}
                type="button"
                onClick={() => setTemperatureUnit(opt.value)}
                className={
                  "px-3 py-1.5 text-xs transition-colors " +
                  (active
                    ? "bg-emerald-600/20 text-emerald-300"
                    : "text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800")
                }
              >
                {opt.label}
              </button>
            );
          })}
        </div>
        <p className="mt-1 text-xs text-neutral-500">
          Only how temperatures are shown changes; Rivolt stores them in Celsius.
        </p>
      </div>

      <div>
        <div className="text-neutral-400 mb-1">Distance</div>
        <div className="inline-flex rounded-md border border-neutral-700 overflow-hidden">
          {distanceOptions.map((opt) => {
            const active = opt.value === distanceUnit;
            return (
              <button
                key={opt.value}
                type="button"
                onClick={() => setDistanceUnit(opt.value)}
                className={
                  "px-3 py-1.5 text-xs transition-colors " +
                  (active
                    ? "bg-emerald-600/20 text-emerald-300"
                    : "text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800")
                }
              >
                {opt.label}
              </button>
            );
          })}
        </div>
        <p className="mt-1 text-xs text-neutral-500">
          Saved to your account, so the web and the iPhone app agree. Temperature follows distance until you choose one.
        </p>
      </div>

      <div>
        <div className="text-neutral-400 mb-1">Time zone</div>
        <select
          value={timeZone}
          onChange={(e) => setTimeZone(e.target.value)}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1.5 text-xs text-neutral-200 focus:border-emerald-500/60 focus:outline-hidden"
        >
          <option value="auto">Auto — browser ({browserZone})</option>
          {zones.map((z) => (
            <option key={z} value={z}>
              {z}
            </option>
          ))}
        </select>
        <p className="mt-1 text-xs text-neutral-500">
          Timestamps are stored in UTC; this only affects how they're displayed.
        </p>
      </div>

      <div>
        <label className="flex items-center gap-2 text-neutral-300">
          <input
            type="checkbox"
            checked={roundTripsEnabled}
            onChange={(e) => setRoundTripsEnabled(e.target.checked)}
            className="h-3.5 w-3.5 accent-emerald-500"
          />
          Merge round-trip drives
        </label>
        <p className="mt-1 text-xs text-neutral-500">
          Collapses consecutive A→B and B→A drives into a single row when the
          return ends near where the first drive started. Raw drive rows in the
          database are never modified — this is a display-only merge.
        </p>
        <div className="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="block text-xs text-neutral-500">
              Radius (meters)
            </span>
            <input
              type="number"
              min={10}
              step={10}
              value={roundTripRadiusMeters}
              disabled={!roundTripsEnabled}
              onChange={(e) => {
                const n = Number(e.target.value);
                if (Number.isFinite(n) && n > 0) setRoundTripRadiusMeters(n);
              }}
              className="mt-0.5 w-28 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-xs text-neutral-200 tabular-nums focus:border-emerald-500/60 focus:outline-hidden disabled:opacity-50"
            />
          </label>
          <label className="block">
            <span className="block text-xs text-neutral-500">
              Max park gap (minutes)
            </span>
            <input
              type="number"
              min={1}
              step={5}
              value={roundTripMaxGapMinutes}
              disabled={!roundTripsEnabled}
              onChange={(e) => {
                const n = Number(e.target.value);
                if (Number.isFinite(n) && n > 0) setRoundTripMaxGapMinutes(n);
              }}
              className="mt-0.5 w-28 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-xs text-neutral-200 tabular-nums focus:border-emerald-500/60 focus:outline-hidden disabled:opacity-50"
            />
          </label>
        </div>
      </div>
    </div>
  );
}

// ChargingCostPanel lets the operator configure the home $/kWh rate
// used to estimate the cost of sessions Rivian reports as free —
// every home-AC / L2 session on non-RAN chargers. Rate × observed
// energy (from the Parallax WS stream) drives estimated_cost on
// /api/charges and /api/live-session responses.
function ChargingCostPanel() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["charging-settings"],
    queryFn: () => backend.getChargingSettings(),
  });
  const [price, setPrice] = useState<string>("");
  const [currency, setCurrency] = useState<string>("USD");
  const [loaded, setLoaded] = useState(false);
  if (!loaded && q.data) {
    setPrice(q.data.home_price_per_kwh ? String(q.data.home_price_per_kwh) : "");
    setCurrency(q.data.home_currency || "USD");
    setLoaded(true);
  }
  const mut = useMutation({
    mutationFn: () =>
      backend.setChargingSettings({
        // Preserve the gas/MPG fields the PlannerPrefsPanel owns. Without
        // this passthrough a save here would zero them out.
        ...(q.data ?? { gas_price_per_gallon: 0, comparison_mpg: 0 }),
        home_price_per_kwh: Number(price) || 0,
        home_currency: currency.toUpperCase() || "USD",
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["charging-settings"] });
      qc.invalidateQueries({ queryKey: ["charges"] });
      qc.invalidateQueries({ queryKey: ["live-session"] });
    },
  });
  if (q.isLoading) return <Spinner />;
  if (q.isError)
    return <ErrorBox title="Failed to load" detail={String(q.error)} />;
  return (
    <form
      className="space-y-3 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        mut.mutate();
      }}
    >
      <div className="flex flex-wrap items-end gap-3">
        <div>
          <label htmlFor="home-price" className="block text-xs text-neutral-400 mb-1">
            Price per kWh
          </label>
          <input
            id="home-price"
            type="number"
            step="0.001"
            min="0"
            inputMode="decimal"
            value={price}
            onChange={(e) => setPrice(e.target.value)}
            placeholder="0.14"
            className="w-28 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
          />
        </div>
        <div>
          <label htmlFor="home-currency" className="block text-xs text-neutral-400 mb-1">
            Currency
          </label>
          <input
            id="home-currency"
            type="text"
            maxLength={3}
            value={currency}
            onChange={(e) => setCurrency(e.target.value.toUpperCase())}
            className="w-20 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 uppercase"
          />
        </div>
        <button
          type="submit"
          disabled={mut.isPending}
          className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
        >
          {mut.isPending ? "Saving…" : "Save"}
        </button>
      </div>
      <p className="text-xs text-neutral-500">
        Applied locally to sessions Rivian reports as free (home AC, Level 2 on
        non-RAN chargers). Leave at 0 to disable.
      </p>
      {mut.isError && <ErrorBox title="Save failed" detail={String(mut.error)} />}
    </form>
  );
}

// ChargingNetworksPanel manages the price book of fast/public
// networks (EVgo, EA, Tesla, etc.) — a flat list of {name, rate,
// currency} rows. The PricingCard on the charge detail page reads
// this list and offers one-click prefill so manual cost entry is
// fast and consistent across sessions on the same network.
// The saved (server) row for a built-in network, only when it has a rate of the user's own.
const savedRateFor = (saved: ChargingNetwork[] | undefined, slug: string) => {
  const n = saved?.find((x) => x.slug === slug);
  return n && networkRateSet(n) ? n : null;
};

function ChargingNetworksPanel() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["charging-networks"],
    queryFn: () => backend.getChargingNetworks(),
  });
  // Local draft state mirrors the server list. We seed once on first
  // successful fetch and let the user mutate freely until they hit
  // Save; cancelling is just a page reload.
  // locDraft is the typed text of a custom row's optional location (#906),
  // parsed on Save; it is never sent.
  const [rows, setRows] = useState<(ChargingNetwork & { locDraft?: string })[]>([]);
  const [loaded, setLoaded] = useState(false);
  if (!loaded && q.data) {
    setRows(q.data);
    setLoaded(true);
  }
  const mut = useMutation({
    mutationFn: () =>
      backend.setChargingNetworks(
        rows.map(({ locDraft, ...r }) => {
          if (locDraft === undefined || r.slug) return r;
          const loc = parseNetworkLocation(locDraft);
          if (!loc.ok) throw new Error(`${r.name || "A network"}: ${loc.why}`);
          return { ...r, lat: loc.lat, lon: loc.lon };
        }),
      ),
    onSuccess: (saved) => {
      setRows(saved);
      qc.invalidateQueries({ queryKey: ["charging-networks"] });
    },
  });
  const update = (i: number, patch: Partial<ChargingNetwork & { locDraft?: string }>) =>
    setRows((prev) => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  const remove = (i: number) =>
    setRows((prev) => prev.filter((_, idx) => idx !== i));
  const add = () =>
    setRows((prev) => [
      ...prev,
      { name: "", price_per_kwh: 0, currency: "USD" },
    ]);
  // After a rate is saved, earlier sessions on that network can be priced at it (Money and the lists refetch).
  const apply = useMutation({
    mutationFn: (slug: string) => backend.applyNetworkRate(slug),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["ledger"] });
      void qc.invalidateQueries({ queryKey: ["charges"] });
    },
  });
  const reset = useMutation({
    mutationFn: () => backend.resetChargingNetworks(),
    onSuccess: (defaults) => {
      setRows(defaults);
      qc.invalidateQueries({ queryKey: ["charging-networks"] });
    },
  });
  // Display order: preferred networks first, then alphabetical by
  // name. Keeps the row indices in `rows` stable for update/remove
  // (callbacks use the original index, not the sorted one).
  const displayOrder = useMemo(
    () =>
      rows
        .map((r, originalIndex) => ({ r, originalIndex }))
        .sort((a, b) => {
          const ap = a.r.preferred ? 1 : 0;
          const bp = b.r.preferred ? 1 : 0;
          if (ap !== bp) return bp - ap;
          return (a.r.name ?? "").localeCompare(b.r.name ?? "", undefined, {
            sensitivity: "base",
          });
        }),
    [rows],
  );

  if (q.isLoading) return <Spinner />;
  if (q.isError)
    return <ErrorBox title="Failed to load" detail={String(q.error)} />;
  return (
    <form
      className="space-y-3 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        mut.mutate();
      }}
    >
      {rows.length === 0 ? (
        <p className="text-xs text-neutral-500">
          No networks configured yet. Add EVgo, Electrify America, or whichever
          fast-charge networks you use most so you can one-click apply their
          rate when pricing a session.
        </p>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          {displayOrder.map(({ r, originalIndex: i }) => {
            const hasMembership = (r.member_price_per_kwh ?? 0) > 0 || !!r.slug;
            // Only when the user entered both prices.
            const savings = memberSaving(r);
            const unset = !networkRateSet(r);
            const isPreferred = !!r.preferred;
            return (
              <div
                key={i}
                className={
                  "rounded-lg border bg-neutral-950/40 p-3 transition-colors " +
                  (isPreferred
                    ? "border-emerald-700/60 ring-1 ring-emerald-700/40"
                    : "border-neutral-800")
                }
              >
                <div className="mb-2 flex items-start gap-2">
                  <input
                    type="text"
                    value={r.name}
                    onChange={(e) => update(i, { name: e.target.value })}
                    placeholder="Network name"
                    className="flex-1 rounded-sm border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm font-medium text-neutral-100"
                  />
                  <button
                    type="button"
                    onClick={() => remove(i)}
                    title="Remove this network"
                    className="rounded-sm border border-neutral-800 bg-neutral-900 px-2 py-1 text-xs text-neutral-500 hover:border-rose-800 hover:text-rose-300"
                  >
                    ×
                  </button>
                </div>

                <div className="flex flex-wrap items-end gap-2">
                  <label className="flex flex-col">
                    <span className="text-[11px] uppercase tracking-wide text-neutral-500">Guest</span>
                    <div className="flex items-center gap-1">
                      <input
                        type="number"
                        step="0.001"
                        min="0"
                        inputMode="decimal"
                        value={r.price_per_kwh || ""}
                        onChange={(e) => update(i, { price_per_kwh: Number(e.target.value) || 0 })}
                        placeholder="0.42"
                        className="w-20 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-100 tabular-nums"
                      />
                      <span className="text-xs text-neutral-500">/kWh</span>
                    </div>
                  </label>
                  {hasMembership && (
                    <label className="flex flex-col">
                      <span className="text-[11px] uppercase tracking-wide text-neutral-500">Member</span>
                      <div className="flex items-center gap-1">
                        <input
                          type="number"
                          step="0.001"
                          min="0"
                          inputMode="decimal"
                          value={r.member_price_per_kwh || ""}
                          onChange={(e) =>
                            update(i, { member_price_per_kwh: Number(e.target.value) || 0 })
                          }
                          placeholder="0.36"
                          className="w-20 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-emerald-300 tabular-nums"
                        />
                        <span className="text-xs text-neutral-500">/kWh</span>
                      </div>
                    </label>
                  )}
                  <label className="flex flex-col">
                    <span className="text-[11px] uppercase tracking-wide text-neutral-500">Cur</span>
                    <input
                      type="text"
                      maxLength={3}
                      value={r.currency}
                      onChange={(e) => update(i, { currency: e.target.value.toUpperCase() })}
                      className="w-14 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 uppercase"
                    />
                  </label>
                  {savings > 0 && (
                    <span className="text-[11px] text-emerald-400 mb-1.5">
                      save {savings.toFixed(2)}/kWh
                    </span>
                  )}
                </div>

                {unset && (
                  <p data-rate-unset className="mt-1.5 text-xs text-neutral-400">
                    {UNSET_RATE_NOTE}
                  </p>
                )}
                {!unset && r.slug && savedRateFor(q.data, r.slug) && (
                  <div className="mt-2 text-xs">
                    <button
                      type="button"
                      disabled={apply.isPending}
                      onClick={() => apply.mutate(r.slug as string)}
                      className="rounded-md border border-neutral-700 px-2.5 py-1 text-neutral-200 hover:border-neutral-500 disabled:opacity-50"
                    >
                      {apply.isPending && apply.variables === r.slug ? "Pricing…" : "Price earlier sessions at this rate"}
                    </button>
                    <span className="ml-2 text-neutral-500">Estimated and unpriced fast sessions only; never a price you entered, a bill or a receipt.</span>
                    {apply.isSuccess && apply.variables === r.slug && (
                      <p role="status" data-apply-result className="mt-1 text-emerald-300">
                        {apply.data.priced > 0 ? `Priced ${apply.data.priced} ${apply.data.priced === 1 ? "session" : "sessions"} at your ${apply.data.network} rate.` : `Nothing to update for ${apply.data.network}.`}
                      </p>
                    )}
                    {apply.isError && apply.variables === r.slug && (
                      <p role="alert" className="mt-1 text-rose-300">Couldn&apos;t price them: {apply.error instanceof Error ? apply.error.message : String(apply.error)}</p>
                    )}
                  </div>
                )}

                {!r.slug && (() => {
                  const parsed = r.locDraft === undefined ? null : parseNetworkLocation(r.locDraft);
                  const bad = parsed && !parsed.ok ? parsed.why : null;
                  const hintId = `net-loc-hint-${i}`;
                  return (
                  <label className="mt-2 flex flex-col text-xs" data-network-location>
                    <span className="text-[11px] uppercase tracking-wide text-neutral-500">Location (optional)</span>
                    <input
                      type="text"
                      inputMode="decimal"
                      value={r.locDraft ?? formatNetworkLocation(r)}
                      onChange={(e) => update(i, { locDraft: e.target.value })}
                      aria-describedby={bad ? `${hintId} ${hintId}-err` : hintId}
                      aria-invalid={bad ? true : undefined}
                      placeholder="40.7128, -74.0060"
                      className={
                        "mt-1 w-full rounded-sm border bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums focus:outline-hidden " +
                        (bad ? "border-rose-500 focus:border-rose-400" : "border-neutral-700 focus:border-neutral-500")
                      }
                    />
                    <span id={hintId} className="mt-1 text-neutral-500">
                      Only used to label your own charges near it. Clear it to remove it.
                    </span>
                    {bad && (
                      <span id={`${hintId}-err`} role="alert" className="mt-0.5 text-rose-400">
                        {bad}
                      </span>
                    )}
                  </label>
                  );
                })()}

                {hasMembership && (
                  <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                    <label className="inline-flex cursor-pointer items-center gap-1.5 text-neutral-300">
                      <input
                        type="checkbox"
                        checked={!!r.member_active}
                        onChange={(e) => update(i, { member_active: e.target.checked })}
                        disabled={!((r.member_price_per_kwh ?? 0) > 0)}
                        className="h-3.5 w-3.5 accent-emerald-500"
                      />
                      <span>
                        I have{" "}
                        <span className="text-neutral-200">{r.member_plan || "this plan"}</span>
                      </span>
                    </label>
                    {r.slug && (
                      <label className="inline-flex cursor-pointer items-center gap-1.5 text-neutral-300">
                        <input
                          type="checkbox"
                          checked={isPreferred}
                          onChange={(e) => update(i, { preferred: e.target.checked })}
                          className="h-3.5 w-3.5 accent-emerald-500"
                        />
                        <span className="inline-flex items-center gap-1">
                          {isPreferred ? "★" : "☆"} Prefer in planner
                        </span>
                      </label>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={add}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-xs text-neutral-200 hover:bg-neutral-800"
        >
          + Add network
        </button>
        <button
          type="submit"
          disabled={mut.isPending}
          className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
        >
          {mut.isPending ? "Saving…" : "Save"}
        </button>
        <button
          type="button"
          onClick={() => {
            if (window.confirm("Reset all networks to the built-in defaults? Your customizations will be lost.")) {
              reset.mutate();
            }
          }}
          disabled={reset.isPending}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-xs text-neutral-400 hover:bg-neutral-800 disabled:opacity-50"
        >
          {reset.isPending ? "Resetting…" : "Reset to defaults"}
        </button>
      </div>
      <p className="text-xs text-neutral-500">
        Member rates and the "I have this plan" toggle feed the trip
        planner cost strip. Guest rate doubles as the one-click prefill
        on each charge's pricing card.
      </p>
      {mut.isError && <ErrorBox title="Save failed" detail={String(mut.error)} />}
      {reset.isError && <ErrorBox title="Reset failed" detail={String(reset.error)} />}
    </form>
  );
}
// import endpoint into a user-facing status line. The row loop emits
// ~20k-row heartbeats (see electrafi.Importer.OnProgress) which is
// what keeps the proxy from idling out on a long CSV.
function formatImportProgress(p: ImportProgress | null): string {
  if (!p) return "Importing…";
  if (p.event === "start") {
    return p.files && p.files > 1 ? `Importing ${p.files} files…` : "Importing…";
  }
  if (p.event === "file_start") {
    return `Reading ${p.file ?? "…"}`;
  }
  if (p.event === "progress") {
    const f = p.file ?? "";
    if (p.phase === "persist_drives") {
      return `Persisting ${p.rows ?? 0} drives · ${f}`;
    }
    if (p.phase === "persist_charges") {
      return `Persisting ${p.rows ?? 0} charges · ${f}`;
    }
    return `${(p.rows ?? 0).toLocaleString()} rows · ${f}`;
  }
  if (p.event === "file_done") {
    return `Finished ${p.file ?? "file"}`;
  }
  return "Importing…";
}

// ImportPanel lets the user drop or pick ElectraFi CSV exports and
// streams them straight to POST /api/import/electrafi. On success we
// invalidate the cached drives/charges/samples so the rest of the app
// reflects the new data without a reload.
function ImportPanel() {
  const qc = useQueryClient();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const [dragging, setDragging] = useState(false);
  // Empty = let the server use electrafi.DefaultPackKWh (single
  // source of truth for the default). Users on Gen 2 / Max /
  // Standard override here.
  const [packKWh, setPackKWh] = useState<string>("");
  const [progress, setProgress] = useState<ImportProgress | null>(null);
  // Picker source. Imports require a real, user-owned vehicle so we
  // never create the legacy `electrafi-<hash>` synthetic rows that
  // used to leak into the lease coordinator.
  const vehiclesQ = useQuery({
    queryKey: ["vehicles", "owned"],
    queryFn: () => backend.listOwnedVehicles(),
  });
  const ownedVehicles = useMemo(() => vehiclesQ.data?.vehicles ?? [], [vehiclesQ.data]);
  const [vehicleID, setVehicleID] = useState<string>("");
  // Default the picker to the first available vehicle once the list
  // loads, so the common single-vehicle case needs zero clicks.
  useEffect(() => {
    if (!vehicleID && ownedVehicles.length > 0) {
      setVehicleID(ownedVehicles[0].rivian_vehicle_id);
    }
  }, [ownedVehicles, vehicleID]);

  // pack_kwh resolution: explicit input first, then the vehicle's
  // recorded pack capacity from /api/vehicles/owned (Rivian-reported
  // usable capacity, not a guess). Falling back to the server's
  // DefaultPackKWh would leave drives with EnergyUsedKWh=0 (the
  // importer deliberately suppresses drive-energy stamping when no
  // pack is provided), so the dashboard would render no cost / no
  // efficiency — the exact bug the user just hit.
  const selectedVehicle = useMemo(
    () => ownedVehicles.find((v) => v.rivian_vehicle_id === vehicleID),
    [ownedVehicles, vehicleID],
  );
  const resolvedPackKWh =
    Number(packKWh) ||
    (selectedVehicle?.pack_kwh && selectedVehicle.pack_kwh > 0
      ? selectedVehicle.pack_kwh
      : undefined);
  const mut = useMutation({
    mutationFn: (files: File[]) =>
      backend.importElectrafi(
        files,
        vehicleID,
        resolvedPackKWh,
        undefined,
        (p) => setProgress(p),
      ),
    onSuccess: () => {
      setProgress(null);
      qc.invalidateQueries({ queryKey: ["drives"] });
      qc.invalidateQueries({ queryKey: ["charges"] });
      qc.invalidateQueries({ queryKey: ["samples"] });
    },
    onError: () => setProgress(null),
  });

  const handleFiles = (fl: FileList | null) => {
    if (!fl || fl.length === 0) return;
    if (!vehicleID) return;
    const files = Array.from(fl).filter((f) => /\.csv$/i.test(f.name));
    if (files.length === 0) return;
    mut.mutate(files);
  };

  const results: ImportResult[] = mut.data?.files ?? [];

  return (
    <div className="space-y-3">
      <div className="flex flex-col gap-1">
        <label htmlFor="import-vehicle" className="text-xs text-neutral-400">
          Import into vehicle
        </label>
        <select
          id="import-vehicle"
          value={vehicleID}
          onChange={(e) => setVehicleID(e.target.value)}
          disabled={vehiclesQ.isLoading || ownedVehicles.length === 0}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-200 disabled:opacity-50"
        >
          {ownedVehicles.length === 0 ? (
            <option value="">No vehicles — link Rivian first</option>
          ) : (
            ownedVehicles.map((v) => (
              <option key={v.rivian_vehicle_id} value={v.rivian_vehicle_id}>
                {v.display_name || v.vin || v.rivian_vehicle_id}
                {v.model_year ? ` · ${v.model_year}` : ""}
                {v.model ? ` ${v.model}` : ""}
              </option>
            ))
          )}
        </select>
        {vehiclesQ.isError && (
          <span className="text-xs text-red-400">
            Failed to load vehicles: {String(vehiclesQ.error)}
          </span>
        )}
      </div>
      <div
        onDragOver={(e) => {
          e.preventDefault();
          if (!vehicleID) return;
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          handleFiles(e.dataTransfer.files);
        }}
        onClick={() => {
          if (!vehicleID) return;
          inputRef.current?.click();
        }}
        className={`rounded-xl border-2 border-dashed p-6 text-center transition-colors ${
          !vehicleID
            ? "border-neutral-800 bg-neutral-900/40 opacity-60 cursor-not-allowed"
            : dragging
            ? "border-emerald-400 bg-emerald-500/5 cursor-pointer"
            : "border-neutral-700 hover:border-neutral-600 cursor-pointer"
        }`}
      >
        <input
          ref={inputRef}
          type="file"
          accept=".csv,text/csv"
          multiple
          className="hidden"
          onChange={(e) => {
            handleFiles(e.target.files);
            e.target.value = "";
          }}
        />
        <div className="text-sm text-neutral-300">
          {mut.isPending ? (
            <span className="inline-flex items-center gap-2">
              <Spinner /> {formatImportProgress(progress)}
            </span>
          ) : (
            <>
              <span className="font-medium text-neutral-200">Drop CSV files here</span>
              <span className="text-neutral-500"> or click to browse</span>
            </>
          )}
        </div>
        <div className="mt-1 text-xs text-neutral-500">
          ElectraFi / TeslaFi exports. Multiple files OK.
        </div>
      </div>

      <div className="flex items-center gap-2 text-xs text-neutral-400">
        <label htmlFor="pack-kwh" className="whitespace-nowrap">
          Pack capacity
        </label>
        <input
          id="pack-kwh"
          type="number"
          step="0.1"
          min="0"
          placeholder={selectedVehicle?.pack_kwh ? String(selectedVehicle.pack_kwh) : "auto"}
          value={packKWh}
          onChange={(e) => setPackKWh(e.target.value)}
          className="w-20 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
        />
        <span>
          kWh · leave blank to use{" "}
          {resolvedPackKWh ? (
            <span className="text-emerald-400">{resolvedPackKWh} kWh from your vehicle</span>
          ) : (
            <span className="text-amber-400">the importer's default (drives won't get energy/cost)</span>
          )}
          {" "}— drives need this to populate energy, cost, and efficiency.
        </span>
      </div>

      {mut.isError && (
        <ErrorBox title="Import failed" detail={String(mut.error)} />
      )}

      {results.length > 0 && (
        <div className="rounded-lg border border-neutral-800 overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-neutral-900 text-neutral-400 text-xs uppercase tracking-wide">
              <tr>
                <th className="text-left px-3 py-2">File</th>
                <th className="text-right px-3 py-2">Rows</th>
                <th className="text-right px-3 py-2">Samples</th>
                <th className="text-right px-3 py-2">Drives</th>
                <th className="text-right px-3 py-2">Charges</th>
                <th className="text-right px-3 py-2">Skipped</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-neutral-800">
              {results.map((r) => (
                <tr key={r.File}>
                  <td className="px-3 py-2 text-neutral-200 truncate max-w-[16rem]">
                    {r.File}
                  </td>
                  <td className="px-3 py-2 text-right text-neutral-300">{r.Rows}</td>
                  <td className="px-3 py-2 text-right text-neutral-300">{r.Samples}</td>
                  <td className="px-3 py-2 text-right text-neutral-300">{r.Drives}</td>
                  <td className="px-3 py-2 text-right text-neutral-300">{r.Charges}</td>
                  <td className="px-3 py-2 text-right text-neutral-500">
                    {r.SkippedRows}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// AI providers
// ---------------------------------------------------------------------------
//
// Mirrors Caffeine's settings UX: one card per provider (OpenAI,
// Anthropic, Gemini) with an API key field and a model dropdown, plus
// a top-level picker that decides which provider is used when multiple
// keys are configured. Keys are write-only — the server reports them
// back as a boolean `has_key` and the UI renders "Key configured" when
// true, so a secret never leaves the backend.
//
// Rivolt only uses text analysis (digest, anomaly explanations, trip
// planner prose) so image / speech pipelines are omitted.

const AI_PROVIDERS: { id: AIProvider; label: string; hint: string }[] = [
  {
    id: "openai",
    label: "OpenAI",
    hint: "GPT-4o family. Paste a key starting with sk-…",
  },
  {
    id: "anthropic",
    label: "Anthropic",
    hint: "Claude family. Paste a key starting with sk-ant-…",
  },
  {
    id: "gemini",
    label: "Google Gemini",
    hint: "Gemini 2.x family. Paste a key from aistudio.google.com",
  },
];

export function AIProvidersPanel() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["ai-settings"],
    queryFn: () => backend.getAISettings(),
  });

  const [selected, setSelected] = useState<"" | AIProvider>("");
  const [keyDrafts, setKeyDrafts] = useState<Record<AIProvider, string>>({
    openai: "",
    anthropic: "",
    gemini: "",
  });
  const [modelDrafts, setModelDrafts] = useState<Record<AIProvider, string>>({
    openai: "",
    anthropic: "",
    gemini: "",
  });

  // Sync drafts from server state on load / after a save round-trips.
  useEffect(() => {
    if (!q.data) return;
    setSelected(q.data.provider ?? "");
    setModelDrafts({
      openai: q.data.providers.openai?.model ?? "",
      anthropic: q.data.providers.anthropic?.model ?? "",
      gemini: q.data.providers.gemini?.model ?? "",
    });
    // Never prefill keys: backend never echoes them back.
    setKeyDrafts({ openai: "", anthropic: "", gemini: "" });
  }, [q.data]);

  const mut = useMutation({
    mutationFn: (patch: AISettingsUpdate) => backend.updateAISettings(patch),
    onSuccess: (fresh) => {
      qc.setQueryData(["ai-settings"], fresh);
    },
  });

  if (q.isLoading) return <Spinner />;
  if (q.isError)
    return <ErrorBox title="Failed to load AI settings" detail={String(q.error)} />;
  if (!q.data) return null;

  const data: AISettings = q.data;

  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <p className="text-sm text-neutral-400">
          Rivolt uses an external LLM only for optional features (weekly digest,
          anomaly explanations, trip planner). Vehicle data never leaves the
          backend except for the specific prompt you invoke.
        </p>
        <div className="flex items-center gap-3 text-sm">
          <label htmlFor="ai-provider" className="text-neutral-400">
            Active provider
          </label>
          <select
            id="ai-provider"
            className="bg-neutral-900 border border-neutral-700 rounded-sm px-2 py-1 text-neutral-100"
            value={selected}
            onChange={(e) => {
              const v = e.target.value as "" | AIProvider;
              setSelected(v);
              mut.mutate({ provider: v });
            }}
          >
            <option value="">Auto (first configured)</option>
            <option value="openai">OpenAI</option>
            <option value="anthropic">Anthropic</option>
            <option value="gemini">Google Gemini</option>
          </select>
          <span
            className={[
              "text-xs px-2 py-0.5 rounded-full border",
              data.ready
                ? "border-emerald-600/40 text-emerald-300 bg-emerald-950/40"
                : "border-neutral-700 text-neutral-400",
            ].join(" ")}
          >
            {data.ready
              ? data.effective_model
                ? `Ready · ${data.effective_model}`
                : "Ready"
              : "Not configured"}
          </span>
          {data.ready ? <AIPingButton /> : null}
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-3">
        {AI_PROVIDERS.map((p) => (
          <ProviderCard
            key={p.id}
            meta={p}
            info={data.providers[p.id]}
            isActive={data.effective_provider === p.id}
            keyDraft={keyDrafts[p.id]}
            modelDraft={modelDrafts[p.id]}
            onKeyDraftChange={(v) =>
              setKeyDrafts((prev) => ({ ...prev, [p.id]: v }))
            }
            onModelDraftChange={(v) =>
              setModelDrafts((prev) => ({ ...prev, [p.id]: v }))
            }
            onSave={(patch) => {
              mut.mutate(patch);
              setKeyDrafts((prev) => ({ ...prev, [p.id]: "" }));
            }}
            onClearKey={() => {
              const patch: AISettingsUpdate = {};
              patch[`${p.id}_api_key` as keyof AISettingsUpdate] = "" as never;
              mut.mutate(patch);
            }}
            saving={mut.isPending}
          />
        ))}
      </div>

      {mut.isError && (
        <ErrorBox title="Save failed" detail={String(mut.error)} />
      )}
    </div>
  );
}

// RecapWeatherPanel owns the single opt-in toggle that lets the
// recap path (and the bulk backfill button below it) hit Open-Meteo
// with each trip's coarse start coords. Lives on its own
// /api/admin/settings/recap surface so it can't be confused for an
// AI-provider config knob.
//
// The backfill button is gated on the toggle: enabling it doesn't
// auto-enrich existing drives, so without an explicit one-shot the
// archive would stay weatherless until each drive's recap is
// (re)generated. Polls the backfill endpoint, which processes a
// bounded batch per call, until `remaining === 0`.
export function RecapWeatherPanel() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["recap-settings"],
    queryFn: () => backend.getRecapSettings(),
  });
  const mut = useMutation({
    mutationFn: (patch: RecapSettingsUpdate) =>
      backend.updateRecapSettings(patch),
    onSuccess: (fresh) => qc.setQueryData(["recap-settings"], fresh),
  });

  // Backfill progress accumulates across polls. We keep the totals
  // in component state rather than a query because the backfill is
  // a sequence of POSTs, not a single fetch — react-query's normal
  // caching shape doesn't fit.
  const [backfill, setBackfill] = useState<{
    running: boolean;
    processed: number;
    succeeded: number;
    failed: number;
    remaining: number | null;
    error: string | null;
    done: boolean;
  }>({
    running: false,
    processed: 0,
    succeeded: 0,
    failed: 0,
    remaining: null,
    error: null,
    done: false,
  });
  // Cancel flag so a click on "Stop" interrupts the loop without
  // awaiting whatever request is currently in flight.
  const cancelRef = useRef(false);

  async function runBackfill() {
    cancelRef.current = false;
    setBackfill({
      running: true,
      processed: 0,
      succeeded: 0,
      failed: 0,
      remaining: null,
      error: null,
      done: false,
    });
    try {
      // Hard ceiling on iterations so a runaway loop (server bug,
      // misreporting `remaining`) can't hammer Open-Meteo forever.
      // Server batch is 25, ceiling = 200 iterations -> 5000 drives.
      // Past that the user can click again.
      for (let i = 0; i < 200; i++) {
        if (cancelRef.current) break;
        const res: DriveWeatherBackfillResult =
          await backend.backfillDriveWeather();
        if (res.disabled) {
          setBackfill((prev) => ({
            ...prev,
            running: false,
            done: true,
            error:
              "Recap weather is disabled. Enable the toggle above and try again.",
          }));
          return;
        }
        setBackfill((prev) => ({
          ...prev,
          processed: prev.processed + res.processed,
          succeeded: prev.succeeded + res.succeeded,
          failed: prev.failed + res.failed,
          remaining: res.remaining,
        }));
        if (res.remaining === 0 || res.processed === 0) break;
      }
      setBackfill((prev) => ({ ...prev, running: false, done: true }));
    } catch (err) {
      setBackfill((prev) => ({
        ...prev,
        running: false,
        error: String(err),
      }));
    }
  }

  if (q.isLoading) return <Spinner />;
  if (q.isError)
    return (
      <ErrorBox
        title="Failed to load recap settings"
        detail={String(q.error)}
      />
    );
  if (!q.data) return null;

  const enabled = q.data.weather_enabled;

  return (
    <div className="space-y-4">
      <p className="text-sm text-neutral-400 max-w-2xl">
        Trip recaps can include the temperature, wind, and precipitation at
        each drive's start so the model can attribute efficiency swings to
        weather instead of inventing the comparison. Enabling this sends
        each drive's start coordinates (rounded to ~11&nbsp;km) and the
        starting hour to Open-Meteo. Off by default.
      </p>
      <label className="flex items-start gap-2 text-sm text-neutral-300 max-w-xl">
        <input
          type="checkbox"
          className="mt-1 accent-emerald-500"
          checked={enabled}
          disabled={mut.isPending}
          onChange={(e) =>
            mut.mutate({ weather_enabled: e.target.checked })
          }
        />
        <span>
          <span className="font-medium text-neutral-200">
            Enrich recaps with weather
          </span>
          <span className="block text-xs text-neutral-500">
            Per-trip lookup happens lazily when a recap is generated. Use
            the backfill below to enrich the existing archive in one go.
          </span>
        </span>
      </label>

      <div className="space-y-2">
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={runBackfill}
            disabled={!enabled || backfill.running}
            className="text-sm px-3 py-1 rounded-sm border border-emerald-600/40 text-emerald-300 hover:bg-emerald-950/40 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {backfill.running
              ? "Backfilling…"
              : "Backfill weather for historical drives"}
          </button>
          {backfill.running && (
            <button
              type="button"
              onClick={() => {
                cancelRef.current = true;
              }}
              className="text-xs px-2 py-0.5 rounded-full border border-neutral-700 text-neutral-300 hover:bg-neutral-800"
            >
              Stop
            </button>
          )}
        </div>
        {!enabled && (
          <p className="text-xs text-neutral-500">
            Enable the toggle first; backfill calls the same Open-Meteo
            endpoint and is blocked while the feature is off.
          </p>
        )}
        {(backfill.running || backfill.done || backfill.error) && (
          <div className="text-xs text-neutral-400 space-y-0.5">
            <div>
              Processed: {backfill.processed} · Succeeded:{" "}
              <span className="text-emerald-400">{backfill.succeeded}</span>
              {backfill.failed > 0 ? (
                <>
                  {" "}
                  · Failed:{" "}
                  <span className="text-amber-400">{backfill.failed}</span>
                </>
              ) : null}
              {backfill.remaining !== null ? (
                <> · Remaining: {backfill.remaining}</>
              ) : null}
            </div>
            {backfill.done && !backfill.error && (
              <div className="text-emerald-400">Backfill complete.</div>
            )}
            {backfill.error && (
              <div className="text-amber-400">{backfill.error}</div>
            )}
          </div>
        )}
      </div>

      {mut.isError && (
        <ErrorBox title="Save failed" detail={String(mut.error)} />
      )}
    </div>
  );
}

// GPSAccuracyPanel exposes the three thresholds the drive detail page
// uses to decide whether to render the "Low GPS accuracy" pill.
// Stored install-wide so a fleet operator can tune for their noise
// floor without a redeploy. Defaults: 40% missing fixes, 5 min stale
// fix age, 2 implausible jumps.
export function GPSAccuracyPanel() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["gps-settings"],
    queryFn: () => backend.getGPSSettings(),
  });
  const mut = useMutation({
    mutationFn: (patch: GPSSettingsUpdate) => backend.updateGPSSettings(patch),
    onSuccess: (fresh) => qc.setQueryData(["gps-settings"], fresh),
  });

  // Local edit state so the user can type without each keystroke
  // firing a PUT. Committed on blur or on Apply.
  const [missingPctStr, setMissingPctStr] = useState("");
  const [staleSecStr, setStaleSecStr] = useState("");
  const [jumpCountStr, setJumpCountStr] = useState("");
  useEffect(() => {
    if (q.data) {
      setMissingPctStr(String(Math.round(q.data.missing_pct * 100)));
      setStaleSecStr(String(q.data.stale_sec));
      setJumpCountStr(String(q.data.jump_count));
    }
  }, [q.data]);

  if (q.isLoading) return <Spinner />;
  if (q.isError)
    return (
      <ErrorBox
        title="Failed to load GPS settings"
        detail={String(q.error)}
      />
    );
  if (!q.data) return null;

  const apply = () => {
    const pct = Number(missingPctStr);
    const sec = Number(staleSecStr);
    const jumps = Number(jumpCountStr);
    if (!Number.isFinite(pct) || !Number.isFinite(sec) || !Number.isFinite(jumps)) return;
    mut.mutate({
      missing_pct: pct / 100,
      stale_sec: Math.max(0, Math.trunc(sec)),
      jump_count: Math.max(1, Math.trunc(jumps)),
    });
  };

  return (
    <div className="space-y-4">
      <p className="text-sm text-neutral-400 max-w-2xl">
        Thresholds for the &quot;Low GPS accuracy&quot; pill on the drive detail
        page. The pill fires when any of these is exceeded: percentage of
        samples with no fix, max stale-fix age, or count of implausible
        spatial jumps (&gt; 0.5&nbsp;mi at &gt; 150&nbsp;mph).
      </p>
      <div className="grid max-w-2xl grid-cols-1 gap-3 sm:grid-cols-3">
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-neutral-400">Missing-fix %</span>
          <input
            type="number"
            min={0}
            max={100}
            value={missingPctStr}
            onChange={(e) => setMissingPctStr(e.target.value)}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
          />
          <span className="text-xs text-neutral-600">flag when &gt; this</span>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-neutral-400">Stale-fix seconds</span>
          <input
            type="number"
            min={0}
            value={staleSecStr}
            onChange={(e) => setStaleSecStr(e.target.value)}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
          />
          <span className="text-xs text-neutral-600">flag when max age &gt; this</span>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-neutral-400">Jump count</span>
          <input
            type="number"
            min={1}
            value={jumpCountStr}
            onChange={(e) => setJumpCountStr(e.target.value)}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
          />
          <span className="text-xs text-neutral-600">flag when ≥ this many</span>
        </label>
      </div>
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={apply}
          disabled={mut.isPending}
          className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-medium text-emerald-50 hover:bg-emerald-800 disabled:bg-neutral-800 disabled:text-neutral-500"
        >
          {mut.isPending ? "Saving…" : "Apply"}
        </button>
        {mut.isSuccess && (
          <span className="text-xs text-emerald-400">Saved · reload the drive page to see the new behavior</span>
        )}
        {mut.isError && (
          <span className="text-xs text-rose-400">{String(mut.error)}</span>
        )}
      </div>
    </div>
  );
}

// AIPingButton sends a trivial prompt to the active provider and
// surfaces the reply + latency + token usage. Purpose is strictly
// diagnostic: confirm the key/model pair produces a 200 from the
// provider so the operator doesn't have to wait for a real feature
// (digest, anomaly, etc.) to exercise the integration.
//
// Error surface is deliberately verbose — a wrong key or a model the
// account doesn't have access to are the two most common failures,
// and both come back with useful messages from the provider. We
// bubble the raw error through so the operator can self-diagnose.
function AIPingButton() {
  const [result, setResult] = useState<AIPingResult | null>(null);
  const mut = useMutation({
    mutationFn: () => backend.pingAI(),
    onSuccess: (r) => setResult(r),
    onError: () => setResult(null),
  });
  return (
    <div className="flex flex-col gap-1">
      <button
        type="button"
        onClick={() => mut.mutate()}
        disabled={mut.isPending}
        className="text-xs px-2 py-0.5 rounded-full border border-emerald-600/40 text-emerald-300 hover:bg-emerald-950/40 disabled:opacity-50"
      >
        {mut.isPending ? "Testing…" : "Test provider"}
      </button>
      {mut.isSuccess && result ? (
        <div className="text-[11px] text-neutral-400 max-w-md">
          <div className="text-emerald-300">{result.reply || "(empty reply)"}</div>
          <div className="mt-0.5 tabular-nums text-neutral-500">
            {result.latency_ms} ms · {result.input_tokens}→{result.output_tokens} tokens
          </div>
        </div>
      ) : null}
      {mut.isError ? (
        <div className="text-[11px] text-red-400/80 max-w-md">
          {String(mut.error)}
        </div>
      ) : null}
    </div>
  );
}

// ProviderCard renders one OpenAI/Anthropic/Gemini tile. It owns the
// model-list query so the fetch only kicks off once the provider has
// a stored key (the list endpoint proxies the provider's own catalogue
// API using the stored credential). When the list is unavailable — no
// key, provider offline, or the endpoint returned an error — the field
// degrades to free-text so the user can still type a model ID by hand.
function ProviderCard({
  meta,
  info,
  isActive,
  keyDraft,
  modelDraft,
  onKeyDraftChange,
  onModelDraftChange,
  onSave,
  onClearKey,
  saving,
}: {
  meta: (typeof AI_PROVIDERS)[number];
  info: { model: string; has_key: boolean } | undefined;
  isActive: boolean;
  keyDraft: string;
  modelDraft: string;
  onKeyDraftChange: (v: string) => void;
  onModelDraftChange: (v: string) => void;
  onSave: (patch: AISettingsUpdate) => void;
  onClearKey: () => void;
  saving: boolean;
}) {
  const models = useQuery({
    queryKey: ["ai-models", meta.id, info?.has_key ? "keyed" : "nokey"],
    queryFn: () => backend.listAIModels(meta.id),
    // Only hit the provider's list endpoint when a key is actually
    // stored; otherwise the backend would return 400 and we'd render
    // a spurious error state.
    enabled: !!info?.has_key,
    staleTime: 10 * 60_000,
    retry: 1,
  });
  const list = models.data?.models ?? [];
  const effectiveModel = modelDraft || info?.model || "";
  const currentInList = modelDraft && list.includes(modelDraft);
  // Free-text fallback applies when the list is empty (loading, no
  // key, or fetch failed) OR when the user already typed a model that
  // doesn't appear in the catalogue (e.g. a preview model the list
  // endpoint hasn't caught up to).
  const useFreeText =
    !info?.has_key || list.length === 0 || (modelDraft !== "" && !currentInList);
  return (
    <div
      className={[
        "rounded-lg border p-3 space-y-2",
        isActive
          ? "border-emerald-600/50 bg-emerald-950/20"
          : "border-neutral-800 bg-neutral-900/40",
      ].join(" ")}
    >
      <div className="flex items-center justify-between">
        <div className="font-medium text-neutral-100">{meta.label}</div>
        <span
          className={[
            "text-xs px-2 py-0.5 rounded-full border",
            info?.has_key
              ? "border-emerald-600/40 text-emerald-300"
              : "border-neutral-700 text-neutral-500",
          ].join(" ")}
        >
          {info?.has_key ? "Key set" : "No key"}
        </span>
      </div>
      <p className="text-xs text-neutral-500">{meta.hint}</p>

      <label className="block text-xs text-neutral-400">
        API key
        <input
          type="password"
          autoComplete="off"
          placeholder={info?.has_key ? "••••••••  (replace to update)" : "paste key"}
          value={keyDraft}
          onChange={(e) => onKeyDraftChange(e.target.value)}
          className="mt-1 w-full bg-neutral-950 border border-neutral-700 rounded-sm px-2 py-1 text-sm text-neutral-100 font-mono"
        />
      </label>

      <label className="block text-xs text-neutral-400">
        <span className="flex items-center justify-between">
          <span>Model</span>
          {info?.has_key && (
            <span className="text-[11px] text-neutral-600">
              {models.isLoading
                ? "loading catalogue…"
                : models.isError
                  ? "catalogue unavailable — free-text"
                  : list.length > 0
                    ? `${list.length} models`
                    : ""}
            </span>
          )}
        </span>
        {useFreeText ? (
          <input
            type="text"
            placeholder={info?.model || "provider default"}
            value={modelDraft}
            onChange={(e) => onModelDraftChange(e.target.value)}
            className="mt-1 w-full bg-neutral-950 border border-neutral-700 rounded-sm px-2 py-1 text-sm text-neutral-100 font-mono"
          />
        ) : (
          <select
            value={modelDraft || info?.model || ""}
            onChange={(e) => onModelDraftChange(e.target.value)}
            className="mt-1 w-full bg-neutral-950 border border-neutral-700 rounded-sm px-2 py-1 text-sm text-neutral-100 font-mono"
          >
            <option value="">provider default</option>
            {list.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        )}
      </label>

      <div className="flex flex-wrap gap-2 pt-1">
        <button
          type="button"
          disabled={saving}
          onClick={() => {
            const patch: AISettingsUpdate = {};
            if (keyDraft.trim().length > 0) {
              patch[`${meta.id}_api_key` as keyof AISettingsUpdate] =
                keyDraft.trim() as never;
            }
            if (modelDraft !== (info?.model ?? "")) {
              patch[`${meta.id}_model` as keyof AISettingsUpdate] =
                modelDraft as never;
            }
            if (Object.keys(patch).length === 0) return;
            onSave(patch);
          }}
          className="text-xs px-2 py-1 rounded-sm border border-emerald-700 bg-emerald-800/40 text-emerald-100 hover:bg-emerald-700/50 disabled:opacity-50"
        >
          Save
        </button>
        {info?.has_key && (
          <button
            type="button"
            disabled={saving}
            onClick={() => {
              if (!window.confirm(`Remove the ${meta.label} API key from Rivolt?`))
                return;
              onClearKey();
            }}
            className="text-xs px-2 py-1 rounded-sm border border-neutral-700 text-neutral-300 hover:bg-neutral-800"
          >
            Clear key
          </button>
        )}
      </div>

      {effectiveModel && (
        <div className="text-[11px] text-neutral-500">
          Using model: <span className="font-mono">{effectiveModel}</span>
        </div>
      )}
    </div>
  );
}

// DangerZonePanel exposes the backup + reset flow. Reset wipes
// every drive, charge, and raw sample for the current user;
// WeeklyRecapCard: the Monday email opt-in. Account-wide, so it needs no push support.
function WeeklyRecapCard() {
  const engagement = useEngagement();
  const [err, setErr] = useState<string | null>(null);
  const [pending, setPending] = useState<boolean | null>(null);
  if (!engagement.settings) return null;
  // Shows the new choice at once; if the save fails the switch goes back and the reason shows.
  const on = pending ?? engagement.settings.weekly_recap;
  return (
    <Card title="Weekly recap" id="weekly-recap">
      <div className="flex items-start justify-between gap-4">
        <div>
          <label htmlFor="weekly-recap-toggle" className="text-sm text-neutral-100">
            Weekly recap by email
          </label>
          <p className="mt-1 text-xs text-neutral-500">
            One short email on Monday with last week&apos;s miles, energy, cost and efficiency, and your lease pace if you track one. Skipped on weeks
            with no driving. Turn off any time.
          </p>
        </div>
        <Toggle
          id="weekly-recap-toggle"
          checked={on}
          onChange={(v) => {
            setErr(null);
            setPending(v);
            engagement
              .save({ weekly_recap: v })
              .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
              .finally(() => setPending(null));
          }}
        />
      </div>
      {err && (
        <p role="alert" className="mt-2 text-xs text-rose-400">
          Couldn&apos;t save: {err}
        </p>
      )}
    </Card>
  );
}

// TripRecapCard: the saved-trip recap notification, off until chosen. Account-wide; it reaches this account's web push and iPhone
// devices that have notifications on. The text names no trip (the app shows it after the tap). Hidden where the server has no such field.
export function TripRecapCard() {
  const engagement = useEngagement();
  const [err, setErr] = useState<string | null>(null);
  const [pending, setPending] = useState<boolean | null>(null);
  if (!engagement.settings || typeof engagement.settings.trip_recap !== "boolean") return null;
  const on = pending ?? engagement.settings.trip_recap;
  return (
    <Card title="Trip recap" id="trip-recap">
      <div className="flex items-start justify-between gap-4">
        <div>
          <label htmlFor="trip-recap-toggle" className="text-sm text-neutral-100">
            Trip recap
          </label>
          <p className="mt-1 text-xs text-neutral-500">
            A notification after you drive a saved trip, showing how it went against the plan. Off until you turn it on.
          </p>
        </div>
        <Toggle
          id="trip-recap-toggle"
          checked={on}
          onChange={(v) => {
            setErr(null);
            setPending(v);
            engagement
              .save({ trip_recap: v })
              .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
              .finally(() => setPending(null));
          }}
        />
      </div>
      {err && (
        <p role="alert" className="mt-2 text-xs text-rose-400">
          Couldn&apos;t save: {err}
        </p>
      )}
    </Card>
  );
}

// AmbientTempCard: the outside-temperature switch, off by default. Not offered in the demo or on a server without it.
function AmbientTempCard() {
  const amb = useAmbientTemp();
  const [pending, setPending] = useState<boolean | null>(null);
  const [err, setErr] = useState<string | null>(null);
  if (amb.enabled === undefined || amb.enabled === null) return null;
  const on = pending ?? amb.enabled;
  return (
    <Card title="Outside temperature" id="ambient-temp">
      <div className="flex items-start justify-between gap-4">
        <div>
          <label htmlFor="ambient-temp-toggle" className="text-sm text-neutral-100">
            Show outside temperature
          </label>
          <p className="mt-1 text-xs text-neutral-500">{AMBIENT_TEMP_TEXT}</p>
        </div>
        <Toggle
          id="ambient-temp-toggle"
          checked={on}
          onChange={(v) => {
            setErr(null);
            setPending(v);
            amb
              .set(v)
              .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
              .finally(() => setPending(null));
          }}
        />
      </div>
      {err && (
        <p role="alert" className="mt-2 text-xs text-rose-400">
          Couldn&apos;t save: {err}
        </p>
      )}
    </Card>
  );
}

// AIFeaturesCard: the user's own AI switch, off by default. Only offered where the server has an AI provider.
function AIFeaturesCard() {
  const serverAI = useAIEnabled();
  const ai = useAIConsent();
  const [pending, setPending] = useState<boolean | null>(null);
  const [err, setErr] = useState<string | null>(null);
  if (!serverAI || ai.consent === undefined) return null;
  const on = pending ?? ai.consent;
  return (
    <Card title="AI features" id="ai-features">
      <div className="flex items-start justify-between gap-4">
        <div>
          <label htmlFor="ai-consent-toggle" className="text-sm text-neutral-100">
            AI trip advice and drive analysis
          </label>
          <p className="mt-1 text-xs text-neutral-500">
            Off by default. When on, tapping Get AI advice on a trip, or Analyze efficiency on a drive, sends data to an AI provider. Trip advice
            sends charger names, your locations rounded to about 11 km, your tire pressures, your home electricity rate and the weather along the
            route. Drive analysis sends the drive&apos;s date and time, its figures (distance, speed, energy, battery, climb and descent), temperatures, weather, tire
            pressures and your vehicle details (tires, wheels, accessories, load, towing); never coordinates or place names. Nothing is sent until
            you tap, and the text you type is never sent. Turn this off any time.
          </p>
        </div>
        <Toggle
          id="ai-consent-toggle"
          checked={on}
          onChange={(v) => {
            setErr(null);
            setPending(v);
            ai.set(v)
              .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
              .finally(() => setPending(null));
          }}
        />
      </div>
      {err && (
        <p role="alert" className="mt-2 text-xs text-rose-400">
          Couldn&apos;t save: {err}
        </p>
      )}
    </Card>
  );
}

// NotificationsPanel handles the Web Push subscription flow for this
// device: permission prompt → fetch VAPID key → pushManager.subscribe
// → POST to /api/push/subscribe. Re-subscribing with new preferences
// updates the existing row (the backend Upserts on endpoint). Test
// button POSTs to /api/push/test which delivers a real notification
// to the registered endpoint so the user can verify end-to-end.
export function NotificationsPanel() {
  const qc = useQueryClient();
  const status = useQuery({
    queryKey: ["push-status"],
    queryFn: () => backend.pushStatus(),
  });

  // Device-local subscription state. Read once on mount from the
  // browser's PushManager so we know whether THIS device is already
  // subscribed (the backend status only reports the aggregate count
  // across all the user's devices).
  const [sub, setSub] = useState<PushSubscription | null>(null);
  const [permission, setPermission] = useState<NotificationPermission>(
    typeof Notification !== "undefined" ? Notification.permission : "denied",
  );
  // The plug-in reminder and milestones start off: they are opt-in, and a device's real switches are
  // read back from the server once the subscription is known.
  const [prefs, setPrefs] = useState<PushPreferences>({
    on_charging_done: true,
    on_plug_in_reminder: false,
    on_anomaly: true,
    on_software_update: true,
    on_milestones: false,
  });
  const engagement = useEngagement();
  const home = useQuery({ queryKey: ["home-location"], queryFn: () => backend.homeLocationGet(), staleTime: 60_000, retry: 0 });
  const [stopNote, setStopNote] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [testedOk, setTestedOk] = useState(false);

  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    void navigator.serviceWorker.ready.then((reg) =>
      reg.pushManager.getSubscription().then(async (s) => {
        setSub(s);
        if (!s) return;
        // What the server has for this device, so the switches are not local guesses. A browser subscription the server never stored
        // (a refused one from before) is not "on": show it off, and Enable registers it again without asking permission twice.
        try {
          const stored = await backend.pushDevice(s.endpoint);
          if (stored) setPrefs(stored);
          else setSub(null);
        } catch {
          /* keep the defaults: the switches still work */
        }
      }),
    );
  }, []);

  const isSupported =
    typeof window !== "undefined" &&
    "serviceWorker" in navigator &&
    "PushManager" in window &&
    "Notification" in window;
  const serverEnabled = status.data?.enabled ?? false;

  async function enable(newPrefs: PushPreferences): Promise<boolean> {
    setErr(null);
    setTestedOk(false);
    setBusy("enable");
    try {
      if (Notification.permission !== "granted") {
        const p = await Notification.requestPermission();
        setPermission(p);
        if (p !== "granted") {
          setErr("Permission denied. Re-enable from browser site settings.");
          return false;
        }
      }
      const reg = await navigator.serviceWorker.ready;
      const { public_key } = await backend.pushVAPIDKey();
      if (!public_key) throw new Error("Server is missing a VAPID public key.");
      // On a refusal the browser subscription made here is removed again, so this panel (and a refresh) shows notifications as off.
      const { sub: s, preferences } = await registerPushSubscription(reg, public_key, newPrefs);
      setPrefs(preferences);
      setSub(s);
      qc.invalidateQueries({ queryKey: ["push-status"] });
      return true;
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setBusy(null);
    }
  }

  // A device switch. The plug-in reminder and milestones go through their own opt-in call (the subscribe
  // upsert ignores them, so a re-save can never turn them on). Turning one on also stores the browser's time
  // zone on the account, or the reminder never fires; if that fails, the switch is put back and the reason shown.
  async function toggle(key: keyof PushPreferences, checked: boolean) {
    const before = prefs;
    const next = { ...prefs, [key]: checked };
    setPrefs(next);
    setStopNote(null);
    setErr(null);
    if (key !== "on_plug_in_reminder" && key !== "on_milestones" && key !== "on_left_open") {
      if (!(await enable(next))) setPrefs(before);
      return;
    }
    if (!sub) {
      setPrefs(before);
      return;
    }
    setBusy("optin");
    try {
      await backend.pushOptin(sub.endpoint, { [key]: checked });
      // The left-open alert needs no time zone (it is not scheduled), so only the other two save one.
      if (checked && key !== "on_left_open") {
        try {
          await engagement.save({});
        } catch (e) {
          const why = e instanceof Error ? e.message : String(e);
          await backend.pushOptin(sub.endpoint, { [key]: false }).catch(() => undefined);
          setPrefs(before);
          setErr(`Couldn't save your time zone, so this reminder was left off: ${why}`);
        }
      }
    } catch (e) {
      setPrefs(before);
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  // Stops one reminder kind on every device when the server can; otherwise on this device only, and says so.
  async function stopAll(kind: "plug_in_reminder" | "milestones" | "left_open") {
    setErr(null);
    setBusy("stop");
    try {
      const done = await backend.pushOff(kind);
      const key = kind === "plug_in_reminder" ? "on_plug_in_reminder" : kind === "left_open" ? "on_left_open" : "on_milestones";
      if (done) {
        setPrefs((p) => ({ ...p, [key]: false }));
        setStopNote("Turned off on all your devices.");
      } else {
        setBusy(null);
        await toggle(key, false);
        setStopNote("Turned off on this device. Other devices keep it until you turn it off there.");
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  async function disable() {
    setErr(null);
    setTestedOk(false);
    setBusy("disable");
    try {
      if (sub) {
        await backend.pushUnsubscribe(sub.endpoint);
        await sub.unsubscribe();
        setSub(null);
      }
      qc.invalidateQueries({ queryKey: ["push-status"] });
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  async function test() {
    setErr(null);
    setTestedOk(false);
    setBusy("test");
    try {
      if (!sub) throw new Error("Subscribe first.");
      await backend.pushTest(sub.endpoint);
      setTestedOk(true);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  if (!isSupported) {
    return (
      <p className="text-sm text-neutral-400 max-w-2xl">
        This browser doesn&apos;t expose the Web Push API. iOS requires the
        site be added to the home screen (Share → Add to Home Screen) before
        notifications can be enabled.
      </p>
    );
  }
  if (status.isLoading) return <Spinner />;
  if (!serverEnabled) {
    return (
      <p className="text-sm text-neutral-400 max-w-2xl">
        Push notifications are not configured on the server (no VAPID keypair).
        Ask the operator to provision one before subscribing.
      </p>
    );
  }

  const subscribed = sub != null;

  return (
    <div className="space-y-4 max-w-2xl">
      <p className="text-sm text-neutral-400">
        Receive a notification on this device when an event fires (charging
        done, plug-in reminder, vehicle anomaly). Each device subscribes
        independently — enable here on every browser / phone you want pinged.
      </p>

      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span
          className={`rounded-full px-2 py-0.5 ${
            subscribed
              ? "border border-emerald-700 bg-emerald-950 text-emerald-300"
              : "border border-neutral-800 bg-neutral-900 text-neutral-400"
          }`}
        >
          {subscribed ? "Subscribed on this device" : "Not subscribed"}
        </span>
        <span className="rounded-full border border-neutral-800 bg-neutral-900 px-2 py-0.5 text-neutral-500">
          Permission: <span className="text-neutral-300">{permission}</span>
        </span>
        <span className="rounded-full border border-neutral-800 bg-neutral-900 px-2 py-0.5 text-neutral-500">
          Devices on account: <span className="text-neutral-300">{status.data?.subscription_count ?? 0}</span>
        </span>
      </div>

      <fieldset
        disabled={!subscribed || busy !== null}
        className="space-y-2 rounded-md border border-neutral-800 bg-neutral-950 p-3 text-sm disabled:opacity-50"
      >
        <legend className="px-1 text-xs text-neutral-500">Notify me when… (on this device)</legend>
        {(
          [
            ["on_charging_done", "Charging session completes", ""],
            ["on_software_update", "Software updates", ""],
            ["on_anomaly", "An anomaly is detected", ""],
            ["on_plug_in_reminder", "Plug-in reminder", "One notification in the evening if the truck is parked at home, unplugged, and below the level you set."],
            ["on_milestones", "Milestones", "A notification when you pass one, like your first 1,000 miles or your 100th charge. Off until you turn it on."],
            ["on_left_open", "Left open", "A notification if a door, the frunk, the liftgate, the tailgate or the tonneau stays open for 15 minutes after the truck is parked and left. Not windows. It can be missed if the truck is offline, so never count on it to tell you the truck is closed. Off until you turn it on."],
          ] as const
        ).map(([key, label, help]) => (
          <label key={key} className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={prefs[key] === true}
              onChange={(e) => void toggle(key, e.target.checked)}
              className="mt-0.5 h-4 w-4 accent-emerald-600"
            />
            <span>
              <span className="block text-neutral-300">{label}</span>
              {help && <span className="block text-xs text-neutral-500">{help}</span>}
            </span>
          </label>
        ))}
      </fieldset>

      {subscribed && prefs.on_plug_in_reminder && engagement.settings && (
        <PlugReminderFields
          settings={engagement.settings}
          hasHome={!!home.data?.set}
          onSave={async (patch) => {
            await engagement.save(patch);
          }}
          onStop={() => void stopAll("plug_in_reminder")}
          stopLabel="Stop these reminders"
          busy={busy !== null}
        />
      )}
      {subscribed && prefs.on_milestones && (
        <button
          type="button"
          onClick={() => void stopAll("milestones")}
          disabled={busy !== null}
          className="text-xs text-neutral-400 underline hover:text-neutral-200 disabled:opacity-50"
        >
          Stop milestone notifications
        </button>
      )}
      {subscribed && prefs.on_left_open && (
        <button
          type="button"
          onClick={() => void stopAll("left_open")}
          disabled={busy !== null}
          className="text-xs text-neutral-400 underline hover:text-neutral-200 disabled:opacity-50"
        >
          Stop left-open notifications
        </button>
      )}
      {stopNote && <p className="text-xs text-emerald-400">{stopNote}</p>}


      <div className="flex flex-wrap items-center gap-3">
        {!subscribed ? (
          <button
            type="button"
            onClick={() => enable(prefs)}
            disabled={busy !== null}
            className="rounded-md bg-emerald-700 px-3 py-1.5 text-sm font-medium text-emerald-50 hover:bg-emerald-800 disabled:bg-neutral-800 disabled:text-neutral-500"
          >
            {busy === "enable" ? "Enabling…" : "Enable on this device"}
          </button>
        ) : (
          <>
            <button
              type="button"
              onClick={test}
              disabled={busy !== null}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50"
            >
              {busy === "test" ? "Sending…" : "Send test"}
            </button>
            <button
              type="button"
              onClick={disable}
              disabled={busy !== null}
              className="rounded-md border border-rose-900 bg-rose-950/40 px-3 py-1.5 text-sm text-rose-300 hover:border-rose-800 disabled:opacity-50"
            >
              {busy === "disable" ? "Disabling…" : "Disable on this device"}
            </button>
          </>
        )}
        {testedOk && (
          <span className="text-xs text-emerald-400">Test delivered — check your notifications.</span>
        )}
      </div>

      {err && (
        <p className="text-xs text-rose-400">{err}</p>
      )}
      <p className="text-xs text-neutral-600">
        Send test delivers a real notification to this device, so you can check it end to end.
      </p>
    </div>
  );
}

// vehicles, settings, push subscriptions, and the user row are
// preserved. Intended for re-importing after changing timezone
// or pack size (the importer's session IDs are tz-derived, so
// otherwise you'd double up). The Backup button fires first and
// is strongly encouraged before Reset.
function DangerZonePanel() {
  const qc = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const [lastBackup, setLastBackup] = useState<{
    filename: string;
    bytes: number;
  } | null>(null);
  const restoreRef = useRef<HTMLInputElement | null>(null);

  const backup = useMutation({
    mutationFn: () => backend.backupData(),
    onSuccess: (res) => setLastBackup(res),
  });
  const reset = useMutation({
    mutationFn: () => backend.resetSessions(),
    onSuccess: () => {
      setConfirm(false);
      // Every list query goes stale now; invalidate the lot.
      qc.invalidateQueries();
    },
  });
  const [restoreProgress, setRestoreProgress] = useState<RestoreProgress | null>(null);
  const restore = useMutation({
    mutationFn: (file: File) => {
      setRestoreProgress(null);
      return backend.restoreData(file, setRestoreProgress);
    },
    onSuccess: () => {
      qc.invalidateQueries();
      if (restoreRef.current) restoreRef.current.value = "";
    },
  });

  const fmtBytes = (n: number): string => {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MiB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(1)} GiB`;
  };

  return (
    <div className="space-y-4 text-sm">
      <div>
        <div className="text-neutral-400 mb-1">Backup</div>
        <p className="text-xs text-neutral-500 mb-2">
          Downloads every drive, charge, and raw sample for your account as a
          single JSON file. Nothing is stored server-side.
        </p>
        <button
          type="button"
          onClick={() => backup.mutate()}
          disabled={backup.isPending}
          className="px-3 py-1.5 text-xs rounded-md border border-neutral-700 bg-neutral-800 text-neutral-200 hover:bg-neutral-700 disabled:opacity-50"
        >
          {backup.isPending ? "Preparing…" : "Download backup"}
        </button>
        {lastBackup && (
          <p className="mt-2 text-xs text-emerald-400">
            Saved {lastBackup.filename} ({fmtBytes(lastBackup.bytes)}).
          </p>
        )}
        {backup.isError && (
          <p className="mt-2 text-xs text-rose-400">
            Backup failed: {String(backup.error)}
          </p>
        )}
      </div>

      <div className="border-t border-neutral-800 pt-4">
        <div className="text-neutral-400 mb-1">Restore</div>
        <p className="text-xs text-neutral-500 mb-2">
          Uploads a <code>rivolt-backup-*.json.gz</code> (or older <code>.json</code>) file and upserts every
          drive, charge, and raw sample. Safe to re-run; existing rows are
          upserted by session ID (drives/charges) or kept as-is (samples).
        </p>
        <input
          ref={restoreRef}
          type="file"
          accept="application/json,application/gzip,.json,.gz"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) restore.mutate(f);
          }}
          disabled={restore.isPending}
          className="text-xs text-neutral-400 file:mr-3 file:px-3 file:py-1.5 file:text-xs file:rounded-md file:border file:border-neutral-700 file:bg-neutral-800 file:text-neutral-200 hover:file:bg-neutral-700 file:cursor-pointer disabled:opacity-50"
        />
        {restore.isPending && (
          <p className="mt-2 text-xs text-neutral-400" role="status">
            Restoring…{restoreSoFar(restoreProgress)} Keep this page open.
          </p>
        )}
        {restore.isSuccess && (
          <p className="mt-2 text-xs text-emerald-400">
            Restored {restore.data.drives} drives, {restore.data.charges}{" "}
            charges, {restore.data.samples} samples.
            {restore.data.skipped
              ? ` ${restore.data.skipped} rows with dates outside 2021 to today were skipped.`
              : ""}
            {restore.data.samples_past_retention
              ? ` ${restore.data.samples_past_retention} detailed samples were older than this server keeps and were left out; their drives and charges were restored.`
              : ""}
          </p>
        )}
        {restore.isError && (
          <p className="mt-2 text-xs text-rose-400" role="alert">
            Restore failed: {String(restore.error)}
            {restorePartialNote(restoreProgress)}
          </p>
        )}
      </div>

      <div className="border-t border-neutral-800 pt-4">
        <div className="text-rose-400 mb-1">Reset sessions</div>
        <p className="text-xs text-neutral-500 mb-2">
          Deletes every drive, charge, and raw sample for your account.
          Vehicles, settings, and push subscriptions are preserved. Useful
          after changing timezone or pack size so a re-import doesn't
          double up (session IDs are timestamp-derived).
        </p>
        {!confirm ? (
          <button
            type="button"
            onClick={() => setConfirm(true)}
            className="px-3 py-1.5 text-xs rounded-md border border-rose-700/50 bg-rose-900/30 text-rose-300 hover:bg-rose-900/50"
          >
            Reset sessions…
          </button>
        ) : (
          <div className="space-y-2">
            <p className="text-xs text-rose-300">
              This can't be undone. Download a backup first if you haven't.
            </p>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => reset.mutate()}
                disabled={reset.isPending}
                className="px-3 py-1.5 text-xs rounded-md bg-rose-600 text-white hover:bg-rose-500 disabled:opacity-50"
              >
                {reset.isPending ? "Deleting…" : "Yes, delete everything"}
              </button>
              <button
                type="button"
                onClick={() => setConfirm(false)}
                disabled={reset.isPending}
                className="px-3 py-1.5 text-xs rounded-md border border-neutral-700 text-neutral-300 hover:bg-neutral-800"
              >
                Cancel
              </button>
            </div>
          </div>
        )}
        {reset.isSuccess && (
          <p className="mt-2 text-xs text-emerald-400">
            Deleted {reset.data.drives} drives, {reset.data.charges} charges,{" "}
            {reset.data.samples} samples.
          </p>
        )}
        {reset.isError && (
          <p className="mt-2 text-xs text-rose-400">
            Reset failed: {String(reset.error)}
          </p>
        )}
      </div>

      <DeleteAccountBlock />
    </div>
  );
}

// DeleteAccountBlock is the self-service "burn it all" path: deletes
// every per-user row (drives, charges, samples, settings, vehicle
// profile, sealed Rivian credentials, push subscriptions, sessions),
// removes the user from the IdP, and signs out. There's a confirm
// gate because the action is irreversible.
function DeleteAccountBlock() {
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function doDelete() {
    setError(null);
    setBusy(true);
    try {
      await backend.deleteAccount();
      // Cookie is cleared by the server; navigate away so the SPA
      // doesn't keep firing /api/me into a 401.
      window.location.assign("/login");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="border-t border-rose-900/60 pt-4 mt-4">
      <div className="text-rose-400 mb-1">Delete account</div>
      <p className="text-xs text-neutral-500 mb-3 max-w-xl">
        Permanently deletes your account and every drive, charge,
        sample, setting, vehicle profile, sealed Rivian credential,
        and push subscription tied to it. Removes you from the
        identity provider. This is irreversible — back up first if
        you want a copy.
      </p>
      {!confirming ? (
        <button
          type="button"
          onClick={() => setConfirming(true)}
          className="rounded-md border border-rose-900 bg-rose-950/40 px-3 py-1.5 text-xs text-rose-300 hover:border-rose-800"
        >
          Delete my account…
        </button>
      ) : (
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={doDelete}
            disabled={busy}
            className="rounded-md bg-rose-700 px-3 py-1.5 text-xs font-semibold text-white hover:bg-rose-600 disabled:opacity-50"
          >
            {busy ? "Deleting…" : "Yes, delete everything"}
          </button>
          <button
            type="button"
            onClick={() => {
              setConfirming(false);
              setError(null);
            }}
            disabled={busy}
            className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-300 hover:border-neutral-500"
          >
            Cancel
          </button>
        </div>
      )}
      {error && (
        <p className="mt-2 text-xs text-rose-400">{error}</p>
      )}
    </div>
  );
}

// AppearanceCard: System / Light / Dark, per device (localStorage).
export function AppearanceCard() {
  const [choice, setChoice] = useThemeChoice();
  const opts: { key: ThemeChoice; label: string }[] = [
    { key: "system", label: "System" },
    { key: "light", label: "Light" },
    { key: "dark", label: "Dark" },
  ];
  return (
    <Card title="Appearance">
      <div role="radiogroup" aria-label="Theme" className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-sm">
        {opts.map((o) => (
          <button
            key={o.key}
            type="button"
            role="radio"
            aria-checked={choice === o.key}
            onClick={() => setChoice(o.key)}
            className={`rounded-md px-3 py-1.5 ${choice === o.key ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
          >
            {o.label}
          </button>
        ))}
      </div>
      <p className="mt-2 text-xs text-neutral-500">System follows your device. Saved on this device only.</p>
    </Card>
  );
}

// homeMoved: the new pin is more than ~100 m from the previous one
// (the same pin re-saved isn't a change worth asking about).
export function homeMoved(
  a: { latitude: number; longitude: number },
  b: { latitude: number; longitude: number },
): boolean {
  const R = 6371000;
  const rad = (d: number) => (d * Math.PI) / 180;
  const dLat = rad(b.latitude - a.latitude);
  const dLon = rad(b.longitude - a.longitude);
  const h = Math.sin(dLat / 2) ** 2 + Math.cos(rad(a.latitude)) * Math.cos(rad(b.latitude)) * Math.sin(dLon / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(h)) > 100;
}

// HomeLocationPanel lets the operator save a "Home" location used by
// the trip planner as a one-click Origin/Destination preset. Same
// geocoding backend as the planner's search box.
export function HomeLocationPanel() {
  const qc = useQueryClient();
  const homeQ = useQuery({
    queryKey: ["settings", "homeLocation"],
    queryFn: backend.homeLocationGet,
    staleTime: 60 * 1000,
  });
  const recheck = useHomeRecheck();
  const saved = (data: HomeLocation, prev: HomeLocation | undefined) => {
    qc.setQueryData(["settings", "homeLocation"], data);
    recheck.reset();
    if (prev?.set && data.set && homeMoved(prev, data)) recheck.start();
  };
  const save = useMutation({
    mutationFn: (h: HomeLocation) => backend.homeLocationPut(h),
    onSuccess: (data) => saved(data, qc.getQueryData<HomeLocation>(["settings", "homeLocation"])),
  });
  const restore = useMutation({
    mutationFn: () => backend.homeLocationRestore(),
    onSuccess: (data) => saved(data, qc.getQueryData<HomeLocation>(["settings", "homeLocation"])),
  });

  const home = homeQ.data;
  // A street address only on this server's own geocoder (Photon); without it the field isn't shown.
  const addressSearch = !!home?.address_search;
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setDebounced(query), 300);
    return () => clearTimeout(t);
  }, [query]);
  const results = useQuery({
    queryKey: ["home-address", debounced],
    queryFn: () => backend.homeAddressSearch(debounced),
    enabled: addressSearch && debounced.trim().length >= 3,
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
  const onSelect = (r: GeocodeResult) => {
    // "Home" everywhere the point appears; the address only as the card's subtitle.
    save.mutate({
      set: true,
      latitude: r.latitude,
      longitude: r.longitude,
      label: "Home",
      address: [r.name, r.admin1, r.country].filter(Boolean).join(", "),
      source: "address",
    });
    setQuery("");
  };

  // Exact coordinates, e.g. pasted from a map app ("32.77666, -96.79699" in the first box works too).
  const [lat, setLat] = useState("");
  const [lon, setLon] = useState("");
  const coords = parseHomeCoordinates(lat, lon);
  const onCoordinates = () => {
    if (!coords.ok) return;
    save.mutate({ set: true, latitude: coords.lat, longitude: coords.lon, label: "Home", address: "", source: "coordinates" });
    setLat("");
    setLon("");
  };
  const onClear = () => {
    save.mutate({ set: false, latitude: 0, longitude: 0, label: "" });
  };

  return (
    <div className="space-y-3 text-sm">
      {homeQ.isLoading ? (
        <Spinner />
      ) : home?.set ? (
        <div className="flex items-baseline justify-between gap-3">
          <div>
            <span className="text-neutral-400">Home: </span>
            <span className="text-neutral-100">{home.label || "Home"}</span>
            {home.address && <div className="text-xs text-neutral-500">{home.address}</div>}
            {home.source && <div data-home-source className="text-xs text-neutral-500">{HOME_SOURCE_TEXT[home.source] ?? ""}</div>}
          </div>
          <button type="button" onClick={onClear} className="text-xs text-neutral-500 hover:text-neutral-300">
            clear
          </button>
        </div>
      ) : (
        <p className="text-neutral-500">Not set. Set it below, or from a charge you made at home.</p>
      )}

      {addressSearch && (
        <div className="relative">
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Home street address"
            placeholder={home?.set ? "Search for a different address…" : "Search for your street address…"}
            className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
          />
          {(results.data ?? []).length > 0 && (
            <ul className="absolute left-0 right-0 top-[calc(100%+2px)] z-10 max-h-64 overflow-y-auto rounded-md border border-neutral-700 bg-neutral-900 shadow-lg">
              {(results.data ?? []).map((r) => (
                <li key={`${r.latitude},${r.longitude}`}>
                  <button type="button" onClick={() => onSelect(r)} className="block w-full px-3 py-2 text-left hover:bg-neutral-800">
                    <div className="text-neutral-100">{r.name}</div>
                    <div className="text-xs text-neutral-500">{[r.admin1, r.country].filter(Boolean).join(", ")}</div>
                  </button>
                </li>
              ))}
            </ul>
          )}
          {/* A cold geocoder can take several seconds on the first search: say it's still looking, then what happened. */}
          {debounced.trim().length >= 3 && (
            <p role="status" aria-live="polite" className="mt-1 text-xs text-neutral-500" data-address-status>
              {results.isFetching ? (
                "Searching…"
              ) : results.isError ? (
                <>
                  Address search didn&apos;t answer.{" "}
                  <button type="button" onClick={() => void results.refetch()} className="underline hover:text-neutral-300">
                    Try again
                  </button>{" "}
                  or enter coordinates below.
                </>
              ) : results.isSuccess && results.data.length === 0 ? (
                "No matches. Try the street and town, or enter coordinates below."
              ) : (
                ""
              )}
            </p>
          )}
        </div>
      )}

      <fieldset className="space-y-2" aria-label="Home coordinates">
        <legend className="text-xs text-neutral-400">Or enter exact coordinates</legend>
        <div className="flex flex-wrap items-center gap-2">
          <input
            type="text"
            inputMode="decimal"
            value={lat}
            onChange={(e) => {
              // A pasted "lat, lon" pair fills both boxes.
              const pair = e.target.value.split(/[,\s]+/).filter(Boolean);
              if (pair.length === 2) {
                setLat(pair[0]);
                setLon(pair[1]);
              } else setLat(e.target.value);
            }}
            aria-label="Latitude"
            placeholder="Latitude, e.g. 32.77666"
            className="w-44 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
          />
          <input
            type="text"
            inputMode="decimal"
            value={lon}
            onChange={(e) => setLon(e.target.value)}
            aria-label="Longitude"
            placeholder="Longitude, e.g. -96.79699"
            className="w-44 rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
          />
          <button
            type="button"
            disabled={!coords.ok || save.isPending}
            onClick={onCoordinates}
            className="rounded-md border border-neutral-700 px-3 py-2 text-xs text-neutral-200 hover:bg-neutral-800 disabled:opacity-50"
          >
            Save coordinates
          </button>
        </div>
        {(lat !== "" || lon !== "") && !coords.ok && <p className="text-xs text-amber-300">{coords.why}</p>}
      </fieldset>

      {home?.previous_set && (
        <button
          type="button"
          disabled={restore.isPending}
          onClick={() => restore.mutate()}
          className="text-xs text-neutral-400 underline hover:text-neutral-200 disabled:opacity-50"
        >
          {restore.isPending ? "Restoring…" : "Restore the previous home"}
        </button>
      )}

      {(save.isError || restore.isError) && (
        <p className="text-xs text-rose-400">Save failed: {String(save.error ?? restore.error)}</p>
      )}
      {recheck.prompt}
      <p className="text-xs text-neutral-500">
        Your home is kept as exactly as you set it.
        {addressSearch ? " Addresses are looked up on this server's own geocoder and aren't sent anywhere else." : ""}
        {" "}On a charge you made at home, “Use this charge's location as home” sets it from where the truck was.
      </p>
    </div>
  );
}

const HOME_SOURCE_TEXT: Record<string, string> = {
  city: "Set from a city search",
  address: "Set from a street address",
  coordinates: "Set from coordinates",
  charge: "Set from a charge's location",
};

// parseHomeCoordinates checks typed coordinates as the server will: finite, latitude -90…90, longitude -180…180.
export function parseHomeCoordinates(lat: string, lon: string): { ok: true; lat: number; lon: number } | { ok: false; why: string } {
  const a = Number(lat.trim());
  const b = Number(lon.trim());
  if (lat.trim() === "" || lon.trim() === "" || !Number.isFinite(a) || !Number.isFinite(b)) {
    return { ok: false, why: "Enter both a latitude and a longitude, as decimal degrees." };
  }
  if (a < -90 || a > 90) return { ok: false, why: "Latitude must be between -90 and 90." };
  if (b < -180 || b > 180) return { ok: false, why: "Longitude must be between -180 and 180." };
  return { ok: true, lat: a, lon: b };
}

// PlannerPrefsPanel saves defaults for the trip planner: drive
// mode and Tesla NACS adapter flag. The planner page reads these
// on load to pre-fill its per-trip form.
function PlannerPrefsPanel() {
  const qc = useQueryClient();
  const prefsQ = useQuery({
    queryKey: ["settings", "plannerPrefs"],
    queryFn: backend.plannerPrefsGet,
    staleTime: 60 * 1000,
  });
  // Gas $/gal + ICE MPG live in ChargingConfig server-side but the user
  // edits them here, next to other trip-planner defaults.
  const chargingQ = useQuery({
    queryKey: ["charging-settings"],
    queryFn: () => backend.getChargingSettings(),
  });
  // Drive the adapter-toggle visibility: if every vehicle on the
  // account would be native NACS by the model-year heuristic, the
  // adapter selector is hidden. The per-vehicle profile override
  // (set in the Vehicle profile panel) is honored at trip-planning
  // time, where the selected vehicle is known.
  const vehiclesQ = useQuery({
    queryKey: ["vehicles", "owned"],
    queryFn: () => backend.listOwnedVehicles(),
    staleTime: 5 * 60_000,
  });
  const anyNeedsAdapter =
    !vehiclesQ.data ||
    vehiclesQ.data.vehicles.length === 0 ||
    vehiclesQ.data.vehicles.some(
      (v) => (v.model_year ?? 0) < 2026,
    );
  const save = useMutation({
    mutationFn: (p: PlannerPrefs) => backend.plannerPrefsPut(p),
    onSuccess: (data) => {
      qc.setQueryData(["settings", "plannerPrefs"], data);
    },
  });
  const saveCharging = useMutation({
    mutationFn: () =>
      backend.setChargingSettings({
        // Preserve the home-rate fields ChargingCostPanel owns.
        ...(chargingQ.data ?? { home_price_per_kwh: 0, home_currency: "USD" }),
        gas_price_per_gallon: Number(gasPrice) || 0,
        comparison_mpg: Number(mpg) || 0,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["charging-settings"] }),
  });

  const [mode, setMode] = useState<PlannerPrefs["drive_mode"]>("");
  const [hasAdapter, setHasAdapter] = useState<"unset" | "yes" | "no">("unset");
  const [gasPrice, setGasPrice] = useState<string>("");
  const [mpg, setMpg] = useState<string>("");

  // Sync local state with loaded prefs on first load.
  const [synced, setSynced] = useState(false);
  useEffect(() => {
    if (synced) return;
    if (!prefsQ.data || !chargingQ.data) return;
    setMode(prefsQ.data.drive_mode || "");
    setHasAdapter(
      typeof prefsQ.data.has_adapter === "boolean"
        ? prefsQ.data.has_adapter ? "yes" : "no"
        : "unset",
    );
    setGasPrice(chargingQ.data.gas_price_per_gallon ? String(chargingQ.data.gas_price_per_gallon) : "");
    setMpg(chargingQ.data.comparison_mpg ? String(chargingQ.data.comparison_mpg) : "");
    setSynced(true);
  }, [prefsQ.data, chargingQ.data, synced]);

  const onSave = () => {
    const payload: PlannerPrefs = { drive_mode: mode };
    if (hasAdapter !== "unset") {
      payload.has_adapter = hasAdapter === "yes";
    }
    save.mutate(payload);
    saveCharging.mutate();
  };

  return (
    <div className="space-y-3 text-sm">
      <label className="flex flex-col gap-1">
        <span className="text-neutral-400">Default drive mode</span>
        <select
          value={mode}
          onChange={(e) => setMode(e.target.value as PlannerPrefs["drive_mode"])}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
        >
          <option value="EVERYDAY">All-Purpose</option>
          <option value="DISTANCE">Conserve</option>
        </select>
      </label>
      {anyNeedsAdapter && (
        <label className="flex flex-col gap-1">
          <span className="text-neutral-400">Tesla NACS adapter</span>
          <select
            value={hasAdapter}
            onChange={(e) =>
              setHasAdapter(e.target.value as "unset" | "yes" | "no")
            }
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden"
          >
            <option value="unset">Not specified (let planner default)</option>
            <option value="yes">Yes — I have it</option>
            <option value="no">No — exclude Superchargers</option>
          </select>
        </label>
      )}
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-neutral-400">Gas $/gal</span>
          <input
            type="number"
            step="0.01"
            min="0"
            inputMode="decimal"
            value={gasPrice}
            onChange={(e) => setGasPrice(e.target.value)}
            placeholder="4.00"
            className="w-24 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
          />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-neutral-400">ICE MPG (comparison)</span>
          <input
            type="number"
            step="1"
            min="0"
            inputMode="decimal"
            value={mpg}
            onChange={(e) => setMpg(e.target.value)}
            placeholder="20"
            className="w-24 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
          />
        </label>
      </div>
      <p className="text-xs text-neutral-500">
        Gas $/gal × ICE MPG drives the gas-equivalent chip on route cards. Leave either at 0 to hide it.
      </p>
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={onSave}
          disabled={save.isPending}
          className="rounded-md bg-emerald-700 px-4 py-2 text-xs font-medium text-emerald-50 hover:bg-emerald-800 disabled:cursor-not-allowed disabled:bg-neutral-800 disabled:text-neutral-500"
        >
          Save defaults
        </button>
        {save.isSuccess && (
          <span className="text-xs text-emerald-400">Saved.</span>
        )}
        {save.isError && (
          <span className="text-xs text-rose-400">
            Save failed: {String(save.error)}
          </span>
        )}
      </div>
    </div>
  );
}

// The signed-out demo shows a read-only settings page (see DemoSettings);
// the real page, with its account, Rivian, push and data sections, is for
// signed-in users.
export default function SettingsPage() {
  return inDemo() ? <DemoSettings /> : <SettingsPageInner />;
}
