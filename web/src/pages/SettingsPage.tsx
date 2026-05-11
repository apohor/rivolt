import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import {
  backend,
  type AIProvider,
  type AISettings,
  type AISettingsUpdate,
  type AIPingResult,
  type ChargingNetwork,
  type DriveWeatherBackfillResult,
  type GeocodeResult,
  type HomeLocation,
  type PlannerPrefs,
  type ImportResult,
  type ImportProgress,
  type RecapSettingsUpdate,
  type GPSSettingsUpdate,
} from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { VehicleProfilePanel } from "../components/VehicleProfilePanel";
import { RivianAccountPanel } from "../components/RivianAccountPanel";
import {
  setTemperatureUnit,
  setTimeZone,
  setRoundTripsEnabled,
  setRoundTripRadiusMeters,
  setRoundTripMaxGapMinutes,
  usePreferences,
  type TemperatureUnit,
} from "../lib/preferences";

export default function SettingsPage() {
  const health = useQuery({ queryKey: ["health"], queryFn: () => backend.health() });

  // Hash scroll. The browser only auto-scrolls to #anchor on a full
  // page load; an SPA navigation from /onboarding → /settings#rivian
  // mounts the page at top and the hash is silently dropped. This
  // effect re-applies it on mount, queued through rAF so React has
  // committed the DOM (the target Card renders before useEffect
  // runs but its layout pass hasn't necessarily flushed).
  const location = useLocation();
  useEffect(() => {
    if (!location.hash) return;
    const id = location.hash.slice(1);
    requestAnimationFrame(() => {
      document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
    });
  }, [location.hash]);

  return (
    <div className="space-y-4">
      <PageHeader title="Settings" />

      <Card title="Backend">
        {health.isLoading ? (
          <Spinner />
        ) : health.isError ? (
          <ErrorBox title="Backend unreachable" detail={String(health.error)} />
        ) : (
          <dl className="text-sm grid grid-cols-[auto,1fr] gap-x-4 gap-y-1">
            <dt className="text-neutral-500">Version</dt>
            <dd className="text-neutral-200">{health.data?.version}</dd>
            <dt className="text-neutral-500">Server time</dt>
            <dd className="text-neutral-200">{health.data?.time}</dd>
          </dl>
        )}
      </Card>

      <Card title="Rivian account" id="rivian">
        <RivianAccountPanel />
      </Card>

      <Card title="Display">
        <DisplayPreferences />
      </Card>

      <Card title="Home charging cost">
        <ChargingCostPanel />
      </Card>

      <Card title="Charging networks">
        <ChargingNetworksPanel />
      </Card>

      <Card title="Home location">
        <HomeLocationPanel />
      </Card>

      <Card title="Trip planner defaults">
        <PlannerPrefsPanel />
      </Card>

      <Card title="Import ElectraFi CSV">
        <ImportPanel />
      </Card>

      <Card title="Vehicle profile">
        <VehicleProfilePanel />
      </Card>

      <Card title="Notifications">
        <p className="text-sm text-neutral-400">
          Push notifications (charging complete, plug-in reminders, anomaly alerts)
          will land once the Rivian ingester is wired. The server-side VAPID keypair
          is already generated and persisted.
        </p>
      </Card>

      <Card title="Danger zone">
        <DangerZonePanel />
      </Card>
    </div>
  );
}

// DisplayPreferences surfaces the client-side display toggles
// (units, etc.) backed by localStorage via usePreferences().
function DisplayPreferences() {
  const {
    temperatureUnit,
    timeZone,
    roundTripsEnabled,
    roundTripRadiusMeters,
    roundTripMaxGapMinutes,
  } = usePreferences();
  const options: { value: TemperatureUnit; label: string }[] = [
    { value: "c", label: "Celsius (°C)" },
    { value: "f", label: "Fahrenheit (°F)" },
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
          Backend always stores Celsius; this only affects how temperatures are
          displayed.
        </p>
      </div>

      <div>
        <div className="text-neutral-400 mb-1">Time zone</div>
        <select
          value={timeZone}
          onChange={(e) => setTimeZone(e.target.value)}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1.5 text-xs text-neutral-200 focus:border-emerald-500/60 focus:outline-none"
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
              className="mt-0.5 w-28 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-xs text-neutral-200 tabular-nums focus:border-emerald-500/60 focus:outline-none disabled:opacity-50"
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
              className="mt-0.5 w-28 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-xs text-neutral-200 tabular-nums focus:border-emerald-500/60 focus:outline-none disabled:opacity-50"
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
            className="w-28 rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
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
            className="w-20 rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 uppercase"
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
        Applied locally to sessions Rivian reports as free (home AC, L2 on
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
function ChargingNetworksPanel() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["charging-networks"],
    queryFn: () => backend.getChargingNetworks(),
  });
  // Local draft state mirrors the server list. We seed once on first
  // successful fetch and let the user mutate freely until they hit
  // Save; cancelling is just a page reload.
  const [rows, setRows] = useState<ChargingNetwork[]>([]);
  const [loaded, setLoaded] = useState(false);
  if (!loaded && q.data) {
    setRows(q.data);
    setLoaded(true);
  }
  const mut = useMutation({
    mutationFn: () => backend.setChargingNetworks(rows),
    onSuccess: (saved) => {
      setRows(saved);
      qc.invalidateQueries({ queryKey: ["charging-networks"] });
    },
  });
  const update = (i: number, patch: Partial<ChargingNetwork>) =>
    setRows((prev) => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  const remove = (i: number) =>
    setRows((prev) => prev.filter((_, idx) => idx !== i));
  const add = () =>
    setRows((prev) => [
      ...prev,
      { name: "", price_per_kwh: 0, currency: "USD" },
    ]);
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
        <div className="space-y-2">
          {rows.map((r, i) => (
            <div key={i} className="flex flex-wrap items-end gap-2">
              <div className="flex-1 min-w-[10rem]">
                <label className="block text-xs text-neutral-400 mb-1">
                  Name
                </label>
                <input
                  type="text"
                  value={r.name}
                  onChange={(e) => update(i, { name: e.target.value })}
                  placeholder="EVgo"
                  className="w-full rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200"
                />
              </div>
              <div>
                <label className="block text-xs text-neutral-400 mb-1">
                  $/kWh
                </label>
                <input
                  type="number"
                  step="0.001"
                  min="0"
                  inputMode="decimal"
                  value={r.price_per_kwh || ""}
                  onChange={(e) =>
                    update(i, { price_per_kwh: Number(e.target.value) || 0 })
                  }
                  placeholder="0.36"
                  className="w-24 rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
                />
              </div>
              <div>
                <label className="block text-xs text-neutral-400 mb-1">
                  Currency
                </label>
                <input
                  type="text"
                  maxLength={3}
                  value={r.currency}
                  onChange={(e) =>
                    update(i, { currency: e.target.value.toUpperCase() })
                  }
                  className="w-20 rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 uppercase"
                />
              </div>
              <button
                type="button"
                onClick={() => remove(i)}
                className="rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-xs text-neutral-400 hover:text-red-300 hover:border-red-700"
              >
                Remove
              </button>
            </div>
          ))}
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
      </div>
      <p className="text-xs text-neutral-500">
        Empty rows and zero-priced rows are dropped on save. Available as
        one-click prefill on each charge's pricing card.
      </p>
      {mut.isError && <ErrorBox title="Save failed" detail={String(mut.error)} />}
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

  const mut = useMutation({
    mutationFn: (files: File[]) =>
      backend.importElectrafi(
        files,
        vehicleID,
        Number(packKWh) || undefined,
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
          placeholder="auto"
          value={packKWh}
          onChange={(e) => setPackKWh(e.target.value)}
          className="w-20 rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
        />
        <span>kWh · leave blank to auto-detect from the vehicle; used to estimate energy when ElectraFi omits <code>charger_power</code> (late-Mar 2026 onward)</span>
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
            className="bg-neutral-900 border border-neutral-700 rounded px-2 py-1 text-neutral-100"
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
            className="text-sm px-3 py-1 rounded border border-emerald-600/40 text-emerald-300 hover:bg-emerald-950/40 disabled:opacity-50 disabled:cursor-not-allowed"
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
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
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
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
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
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
          />
          <span className="text-xs text-neutral-600">flag when ≥ this many</span>
        </label>
      </div>
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={apply}
          disabled={mut.isPending}
          className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-medium text-emerald-50 hover:bg-emerald-600 disabled:bg-neutral-800 disabled:text-neutral-500"
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
          className="mt-1 w-full bg-neutral-950 border border-neutral-700 rounded px-2 py-1 text-sm text-neutral-100 font-mono"
        />
      </label>

      <label className="block text-xs text-neutral-400">
        <span className="flex items-center justify-between">
          <span>Model</span>
          {info?.has_key && (
            <span className="text-[10px] text-neutral-600">
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
            className="mt-1 w-full bg-neutral-950 border border-neutral-700 rounded px-2 py-1 text-sm text-neutral-100 font-mono"
          />
        ) : (
          <select
            value={modelDraft || info?.model || ""}
            onChange={(e) => onModelDraftChange(e.target.value)}
            className="mt-1 w-full bg-neutral-950 border border-neutral-700 rounded px-2 py-1 text-sm text-neutral-100 font-mono"
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
          className="text-xs px-2 py-1 rounded border border-emerald-700 bg-emerald-800/40 text-emerald-100 hover:bg-emerald-700/50 disabled:opacity-50"
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
            className="text-xs px-2 py-1 rounded border border-neutral-700 text-neutral-300 hover:bg-neutral-800"
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
  const restore = useMutation({
    mutationFn: (file: File) => backend.restoreData(file),
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
          Uploads a <code>rivolt-backup-*.json</code> file and upserts every
          drive, charge, and raw sample. Safe to re-run; existing rows are
          upserted by session ID (drives/charges) or kept as-is (samples).
        </p>
        <input
          ref={restoreRef}
          type="file"
          accept="application/json,.json"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) restore.mutate(f);
          }}
          disabled={restore.isPending}
          className="text-xs text-neutral-400 file:mr-3 file:px-3 file:py-1.5 file:text-xs file:rounded-md file:border file:border-neutral-700 file:bg-neutral-800 file:text-neutral-200 hover:file:bg-neutral-700 file:cursor-pointer disabled:opacity-50"
        />
        {restore.isPending && (
          <p className="mt-2 text-xs text-neutral-400">Restoring…</p>
        )}
        {restore.isSuccess && (
          <p className="mt-2 text-xs text-emerald-400">
            Restored {restore.data.drives} drives, {restore.data.charges}{" "}
            charges, {restore.data.samples} samples.
          </p>
        )}
        {restore.isError && (
          <p className="mt-2 text-xs text-rose-400">
            Restore failed: {String(restore.error)}
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
    </div>
  );
}

// HomeLocationPanel lets the operator save a "Home" location used by
// the trip planner as a one-click Origin/Destination preset. Same
// geocoding backend as the planner's search box.
function HomeLocationPanel() {
  const qc = useQueryClient();
  const homeQ = useQuery({
    queryKey: ["settings", "homeLocation"],
    queryFn: backend.homeLocationGet,
    staleTime: 60 * 1000,
  });
  const save = useMutation({
    mutationFn: (h: HomeLocation) => backend.homeLocationPut(h),
    onSuccess: (data) => {
      qc.setQueryData(["settings", "homeLocation"], data);
    },
  });

  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setDebounced(query), 300);
    return () => clearTimeout(t);
  }, [query]);
  const results = useQuery({
    queryKey: ["geocode", debounced],
    queryFn: () => backend.geocode(debounced, 5),
    enabled: debounced.trim().length >= 2,
    staleTime: 5 * 60 * 1000,
  });

  const home = homeQ.data;
  const onSelect = (r: GeocodeResult) => {
    save.mutate({
      set: true,
      latitude: r.latitude,
      longitude: r.longitude,
      label: [r.name, r.admin1, r.country].filter(Boolean).join(", "),
    });
    setQuery("");
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
            <span className="text-neutral-100">{home.label || "(unnamed)"}</span>
          </div>
          <button
            type="button"
            onClick={onClear}
            className="text-xs text-neutral-500 hover:text-neutral-300"
          >
            clear
          </button>
        </div>
      ) : (
        <p className="text-neutral-500">
          Not set. Pick a place below to enable the Home preset on the trip planner.
        </p>
      )}

      <div className="relative">
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={home?.set ? "Search for a different home…" : "Search for your home city…"}
          className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
        />
        {(results.data ?? []).length > 0 && (
          <ul className="absolute left-0 right-0 top-[calc(100%+2px)] z-10 max-h-64 overflow-y-auto rounded-md border border-neutral-700 bg-neutral-900 shadow-lg">
            {(results.data ?? []).map((r) => (
              <li key={`${r.latitude},${r.longitude}`}>
                <button
                  type="button"
                  onClick={() => onSelect(r)}
                  className="block w-full px-3 py-2 text-left hover:bg-neutral-800"
                >
                  <div className="text-neutral-100">{r.name}</div>
                  <div className="text-xs text-neutral-500">
                    {[r.admin1, r.country].filter(Boolean).join(", ")}
                  </div>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      {save.isError && (
        <p className="text-xs text-rose-400">Save failed: {String(save.error)}</p>
      )}
      <p className="text-xs text-neutral-500">
        Open-Meteo geocoding (city-level). Street addresses arrive when self-hosted Photon ships.
      </p>
    </div>
  );
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
  const save = useMutation({
    mutationFn: (p: PlannerPrefs) => backend.plannerPrefsPut(p),
    onSuccess: (data) => {
      qc.setQueryData(["settings", "plannerPrefs"], data);
    },
  });

  const [mode, setMode] = useState<PlannerPrefs["drive_mode"]>("");
  const [hasAdapter, setHasAdapter] = useState<"unset" | "yes" | "no">("unset");

  // Sync local state with loaded prefs on first load.
  const [synced, setSynced] = useState(false);
  useEffect(() => {
    if (synced) return;
    if (!prefsQ.data) return;
    setMode(prefsQ.data.drive_mode || "");
    setHasAdapter(
      typeof prefsQ.data.has_adapter === "boolean"
        ? prefsQ.data.has_adapter ? "yes" : "no"
        : "unset",
    );
    setSynced(true);
  }, [prefsQ.data, synced]);

  const onSave = () => {
    const payload: PlannerPrefs = { drive_mode: mode };
    if (hasAdapter !== "unset") {
      payload.has_adapter = hasAdapter === "yes";
    }
    save.mutate(payload);
  };

  return (
    <div className="space-y-3 text-sm">
      <label className="flex flex-col gap-1">
        <span className="text-neutral-400">Default drive mode</span>
        <select
          value={mode}
          onChange={(e) => setMode(e.target.value as PlannerPrefs["drive_mode"])}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
        >
          <option value="">No default (let planner pick)</option>
          <option value="EVERYDAY">All-Purpose</option>
          <option value="DISTANCE">Conserve</option>
          <option value="SPORT">Sport</option>
          <option value="WINTER">Snow</option>
          <option value="OFF_ROAD_AUTO">All-Terrain</option>
        </select>
      </label>
      <label className="flex flex-col gap-1">
        <span className="text-neutral-400">Tesla NACS adapter</span>
        <select
          value={hasAdapter}
          onChange={(e) =>
            setHasAdapter(e.target.value as "unset" | "yes" | "no")
          }
          className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-none"
        >
          <option value="unset">Not specified (let planner default)</option>
          <option value="yes">Yes — I have it</option>
          <option value="no">No — exclude Superchargers</option>
        </select>
      </label>
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={onSave}
          disabled={save.isPending}
          className="rounded-md bg-emerald-700 px-4 py-2 text-xs font-medium text-emerald-50 hover:bg-emerald-600 disabled:cursor-not-allowed disabled:bg-neutral-800 disabled:text-neutral-500"
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
