import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { backend, type ImportResult } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { RivianAccountPanel } from "../components/RivianAccountPanel";
import {
  setTemperatureUnit,
  setTimeZone,
  usePreferences,
  type TemperatureUnit,
} from "../lib/preferences";

export default function SettingsPage() {
  const health = useQuery({ queryKey: ["health"], queryFn: () => backend.health() });

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

      <Card title="Rivian account">
        <RivianAccountPanel />
      </Card>

      <Card title="Display">
        <DisplayPreferences />
      </Card>

      <Card title="Home charging cost">
        <ChargingCostPanel />
      </Card>

      <Card title="Import ElectraFi CSV">
        <ImportPanel />
      </Card>

      <Card title="Notifications">
        <p className="text-sm text-neutral-400">
          Push notifications (charging complete, plug-in reminders, anomaly alerts)
          will land once the Rivian ingester is wired. The server-side VAPID keypair
          is already generated and persisted.
        </p>
      </Card>
    </div>
  );
}

// DisplayPreferences surfaces the client-side display toggles
// (units, etc.) backed by localStorage via usePreferences().
function DisplayPreferences() {
  const { temperatureUnit, timeZone } = usePreferences();
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

// ImportPanel lets the user drop or pick ElectraFi CSV exports and
// streams them straight to POST /api/import/electrafi. On success we
// invalidate the cached drives/charges/samples so the rest of the app
// reflects the new data without a reload.
function ImportPanel() {
  const qc = useQueryClient();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const [dragging, setDragging] = useState(false);
  const [packKWh, setPackKWh] = useState<string>("141.5");

  const mut = useMutation({
    mutationFn: (files: File[]) =>
      backend.importElectrafi(files, Number(packKWh) || undefined),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["drives"] });
      qc.invalidateQueries({ queryKey: ["charges"] });
      qc.invalidateQueries({ queryKey: ["samples"] });
    },
  });

  const handleFiles = (fl: FileList | null) => {
    if (!fl || fl.length === 0) return;
    const files = Array.from(fl).filter((f) => /\.csv$/i.test(f.name));
    if (files.length === 0) return;
    mut.mutate(files);
  };

  const results: ImportResult[] = mut.data?.files ?? [];

  return (
    <div className="space-y-3">
      <div
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          handleFiles(e.dataTransfer.files);
        }}
        onClick={() => inputRef.current?.click()}
        className={`rounded-xl border-2 border-dashed p-6 text-center cursor-pointer transition-colors ${
          dragging
            ? "border-emerald-400 bg-emerald-500/5"
            : "border-neutral-700 hover:border-neutral-600"
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
              <Spinner /> Importing…
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
          value={packKWh}
          onChange={(e) => setPackKWh(e.target.value)}
          className="w-20 rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
        />
        <span>kWh · used to estimate energy when ElectraFi omits <code>charger_power</code> (late-Mar 2026 onward)</span>
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
