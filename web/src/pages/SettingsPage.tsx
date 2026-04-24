import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import {
  backend,
  type AIProvider,
  type AISettings,
  type AISettingsUpdate,
  type ImportResult,
} from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
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

      <Card title="AI providers">
        <AIProvidersPanel />
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

function AIProvidersPanel() {
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
