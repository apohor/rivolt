import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useLocation } from "react-router-dom";
import {
  aiModels,
  backend,
  machine,
  MachineError,
  type AIProvider,
  type AIImageProvider,
  type AISpeechProvider,
  type AISettings,
  type AISettingsUpdate,
  type AIUsageCostBreak,
  type MachineSettings,
} from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner, Toggle } from "../components/ui";
import PreheatSection from "../components/PreheatSection";
import NotificationsSection from "../components/NotificationsSection";

// Curated list of commonly-edited machine settings. Anything the machine
// reports that isn't in this list falls through to the "Advanced" read-only
// card so nothing is silently dropped. Field types mirror
// meticulous-typescript-api `Settings` (note: auto_preheat is a *number* —
// boiler hold temperature in °C — not a boolean).
type KnownEntry = {
  key: string;
  label: string;
  kind: "string" | "boolean" | "number" | "enum";
  // For enum: list of allowed values rendered as a <select>.
  options?: string[];
  // Hint shown under the label.
  hint?: string;
};

const KNOWN: KnownEntry[] = [
  { key: "time_zone", label: "Time zone", kind: "string" },
  {
    key: "timezone_sync",
    label: "Time-zone sync",
    kind: "enum",
    options: ["automatic", "manual"],
  },
  { key: "update_channel", label: "Update channel", kind: "string" },
  { key: "enable_sounds", label: "Sounds", kind: "boolean" },
  { key: "heat_on_boot", label: "Heat on boot", kind: "boolean" },
  { key: "auto_preheat", label: "Auto-preheat hold (°C)", kind: "number" },
  {
    key: "heating_timeout",
    label: "Heating timeout (min)",
    kind: "number",
    hint: "Minutes before the boiler gives up trying to reach target",
  },
  { key: "auto_purge_after_shot", label: "Auto-purge after shot", kind: "boolean" },
  { key: "auto_start_shot", label: "Auto-start shot", kind: "boolean" },
  {
    key: "allow_stage_skipping",
    label: "Allow stage skipping",
    kind: "boolean",
    hint: "Tap to advance past a profile stage mid-shot",
  },
  {
    key: "partial_retraction",
    label: "Partial retraction (mm)",
    kind: "number",
  },
  { key: "clock_format_24_hour", label: "24-hour clock", kind: "boolean" },
  {
    key: "idle_screen",
    label: "Idle screen",
    kind: "enum",
    options: ["metCat", "clock", "off"],
  },
  { key: "save_debug_shot_data", label: "Save debug shot data", kind: "boolean" },
  { key: "debug_shot_data_retention_days", label: "Debug data retention (days)", kind: "number" },
  {
    key: "allow_debug_sending",
    label: "Send debug reports",
    kind: "boolean",
    hint: "Upload crash/debug data to Meticulous",
  },
  { key: "ssh_enabled", label: "SSH access", kind: "boolean" },
  {
    key: "hostname_override",
    label: "Hostname override",
    kind: "string",
    hint: "Leave blank for machine default",
  },
  {
    key: "usb_mode",
    label: "USB mode",
    kind: "enum",
    options: ["host", "device"],
  },
  { key: "telemetry_service_enabled", label: "Telemetry", kind: "boolean" },
];

export default function SettingsPage() {
  const qc = useQueryClient();
  const location = useLocation();
  const { data, isLoading, error } = useQuery({
    queryKey: ["settings"],
    queryFn: () => machine.getSettings(),
  });

  // React Router v6 doesn't scroll to #hash on navigation. Do it ourselves
  // after the section mounts so deep links like /settings#preheat-schedule
  // land on the card.
  useEffect(() => {
    if (!location.hash) return;
    const id = location.hash.replace(/^#/, "");
    // Wait a tick for the section to render (queries may still be loading).
    const t = setTimeout(() => {
      const el = document.getElementById(id);
      if (el) el.scrollIntoView({ behavior: "smooth", block: "start" });
    }, 50);
    return () => clearTimeout(t);
  }, [location.hash]);

  const [draft, setDraft] = useState<MachineSettings | null>(null);
  const working = draft ?? data ?? null;
  const dirty = useMemo(
    () => !!draft && !!data && JSON.stringify(draft) !== JSON.stringify(data),
    [draft, data],
  );

  const save = useMutation({
    mutationFn: async (patch: MachineSettings) => {
      // The machine accepts single-key patches. Send each dirty field.
      const results: MachineSettings[] = [];
      for (const [key, value] of Object.entries(patch)) {
        results.push(await machine.updateSetting(key, value));
      }
      return results[results.length - 1] ?? {};
    },
    onSuccess: async () => {
      setDraft(null);
      await qc.invalidateQueries({ queryKey: ["settings"] });
    },
  });

  const diff: MachineSettings = useMemo(() => {
    if (!draft || !data) return {};
    const out: MachineSettings = {};
    for (const k of Object.keys(draft)) {
      if (JSON.stringify(draft[k]) !== JSON.stringify((data as MachineSettings)[k])) {
        out[k] = draft[k];
      }
    }
    return out;
  }, [draft, data]);

  function setKey(key: string, value: unknown) {
    const base = draft ?? (data ? { ...data } : null);
    if (!base) return;
    setDraft({ ...base, [key]: value });
  }

  const unknownKeys = working
    ? Object.keys(working).filter((k) => !KNOWN.some((e) => e.key === k))
    : [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Settings"
        subtitle="AI analysis keys live on the server; machine settings live on the machine."
        actions={
          <div className="flex gap-2">
            <button
              type="button"
              disabled={!dirty}
              onClick={() => setDraft(null)}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800 disabled:opacity-40"
            >
              Discard
            </button>
            <button
              type="button"
              disabled={!dirty || save.isPending}
              onClick={() => save.mutate(diff)}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
            >
              {save.isPending ? "saving…" : "Save machine settings"}
            </button>
          </div>
        }
      />

      <AICard />
      <NotificationsSection />
      <PreheatSection />
      {isLoading && <Spinner />}
      {error && (
        <ErrorBox
          title="Could not load settings"
          detail={
            error instanceof MachineError
              ? `${error.status}: ${JSON.stringify(error.body)}`
              : String(error)
          }
        />
      )}
      {save.error && (
        <ErrorBox
          title="Save failed"
          detail={
            save.error instanceof MachineError
              ? `${save.error.status}: ${JSON.stringify(save.error.body)}`
              : String(save.error)
          }
        />
      )}

      {working && (
        <>
          <Card title="Machine preferences">
            <ul className="divide-y divide-neutral-800/60">
              {KNOWN.map((entry) => {
                const value = working[entry.key];
                if (value === undefined) return null;
                return (
                  <li
                    key={entry.key}
                    className="flex items-center gap-4 py-3 first:pt-0 last:pb-0"
                  >
                    <div className="min-w-0 flex-1">
                      <div className="text-sm text-neutral-100">{entry.label}</div>
                      {entry.hint && (
                        <div className="mt-0.5 text-[11px] text-neutral-500">{entry.hint}</div>
                      )}
                      <div className="mt-0.5 text-[11px] font-mono text-neutral-500">
                        {entry.key}
                      </div>
                    </div>
                    <div className="shrink-0">
                      {entry.kind === "boolean" && (
                        <Toggle
                          checked={!!value}
                          onChange={(v) => setKey(entry.key, v)}
                        />
                      )}
                      {entry.kind === "string" && (
                        <input
                          type="text"
                          value={String(value ?? "")}
                          onChange={(e) =>
                            setKey(entry.key, e.target.value === "" ? null : e.target.value)
                          }
                          className="input w-56"
                        />
                      )}
                      {entry.kind === "number" && (
                        <input
                          type="number"
                          value={Number(value ?? 0)}
                          onChange={(e) => setKey(entry.key, Number(e.target.value))}
                          className="input w-28"
                        />
                      )}
                      {entry.kind === "enum" && (
                        <select
                          value={String(value ?? "")}
                          onChange={(e) => setKey(entry.key, e.target.value)}
                          className="input w-40"
                        >
                          {entry.options?.map((opt) => (
                            <option key={opt} value={opt}>
                              {opt}
                            </option>
                          ))}
                        </select>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
          </Card>

          {unknownKeys.length > 0 && (
            <Card title={`Advanced (read-only · ${unknownKeys.length})`}>
              <p className="mb-3 text-xs text-neutral-500">
                Nested or list-shaped settings that need a dedicated UI (e.g.
                profile ordering, per-surface scroll direction). Edit via the
                Meticulous app for now.
              </p>
              <ul className="divide-y divide-neutral-800/60">
                {unknownKeys.sort().map((k) => (
                  <li
                    key={k}
                    className="flex items-start gap-4 py-2 first:pt-0 last:pb-0"
                  >
                    <div className="min-w-0 flex-1 font-mono text-xs text-neutral-400">
                      {k}
                    </div>
                    <div className="shrink-0 max-w-[55%] truncate font-mono text-xs text-neutral-300">
                      <ReadOnlyValue value={working[k]} />
                    </div>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </>
      )}
    </div>
  );
}

// ReadOnlyValue renders a setting value with some mild formatting so the
// Advanced card doesn't look like raw JSON soup.
function ReadOnlyValue({ value }: { value: unknown }) {
  if (value === null || value === undefined) {
    return <span className="text-neutral-600">—</span>;
  }
  if (typeof value === "boolean") {
    return (
      <span className={value ? "text-emerald-400" : "text-neutral-500"}>
        {value ? "on" : "off"}
      </span>
    );
  }
  if (typeof value === "number" || typeof value === "string") {
    return <>{String(value)}</>;
  }
  return <span title={JSON.stringify(value)}>{JSON.stringify(value)}</span>;
}

// ---------------------------------------------------------------------------
// AICard: provider/model/API-key configuration for the LLM that produces
// shot critiques. API keys are never echoed back from the server; the UI
// only sees a { has_key: boolean } per provider. An empty input leaves the
// stored key alone; typing something new replaces it.
// ---------------------------------------------------------------------------

type ProviderLabel = { id: AIProvider; label: string; placeholder: string };

const PROVIDERS: ProviderLabel[] = [
  { id: "openai", label: "OpenAI", placeholder: "sk-…" },
  { id: "anthropic", label: "Anthropic", placeholder: "sk-ant-…" },
  { id: "gemini", label: "Google Gemini", placeholder: "AIza…" },
];

function AICard() {
  const qc = useQueryClient();
  const { data, isLoading, error } = useQuery({
    queryKey: ["ai-settings"],
    queryFn: () => backend.getAISettings(),
  });
  // Real token usage per provider for the last 30 days. Shown inline on
  // each provider tile so it sits right next to the key that generated it.
  const usageQ = useQuery({
    queryKey: ["ai-usage", 30],
    queryFn: () => backend.getAIUsage(30, 1),
    refetchInterval: 60_000,
  });

  // Local draft: models per-provider + pending key replacements. Keys start
  // as empty strings; the submit logic only sends the ones the user typed.
  const [draft, setDraft] = useState<{
    provider: "" | AIProvider;
    models: Record<AIProvider, string>;
    newKeys: Partial<Record<AIProvider, string>>;
    imageProvider: "" | AIImageProvider;
    imageModels: Record<AIImageProvider, string>;
    speechProvider: "" | AISpeechProvider;
    speechModels: Record<AISpeechProvider, string>;
  } | null>(null);

  const working = useMemo(() => {
    if (!data) return null;
    // Defend against an older backend that doesn't emit the `image` field.
    const img = data.image ?? { provider: "" as const, ready: false };
    const sp = data.speech ?? { provider: "" as const, ready: false };
    return (
      draft ?? {
        provider: data.provider,
        models: {
          openai: data.providers.openai.model,
          anthropic: data.providers.anthropic.model,
          gemini: data.providers.gemini.model,
        },
        newKeys: {},
        imageProvider: img.provider,
        imageModels: {
          openai: img.openai_model ?? "",
          gemini: img.gemini_model ?? "",
        },
        speechProvider: sp.provider,
        speechModels: {
          openai: sp.openai_model ?? "",
          gemini: sp.gemini_model ?? "",
        },
      }
    );
  }, [data, draft]);

  const dirty = !!draft;

  const save = useMutation({
    mutationFn: async (patch: AISettingsUpdate) => backend.updateAISettings(patch),
    onSuccess: async () => {
      setDraft(null);
      await qc.invalidateQueries({ queryKey: ["ai-settings"] });
    },
  });

  function setProvider(p: "" | AIProvider) {
    setDraft({ ...working!, provider: p });
  }
  function setModel(p: AIProvider, v: string) {
    setDraft({
      ...working!,
      models: { ...working!.models, [p]: v },
    });
  }
  function setKey(p: AIProvider, v: string) {
    setDraft({
      ...working!,
      newKeys: { ...working!.newKeys, [p]: v },
    });
  }
  function clearKey(p: AIProvider) {
    // Explicit empty string tells the server to clear the stored key.
    setDraft({
      ...working!,
      newKeys: { ...working!.newKeys, [p]: "" },
    });
  }
  function setImageProvider(p: "" | AIImageProvider) {
    setDraft({ ...working!, imageProvider: p });
  }
  function setImageModel(p: AIImageProvider, v: string) {
    setDraft({
      ...working!,
      imageModels: { ...working!.imageModels, [p]: v },
    });
  }
  function setSpeechProvider(p: "" | AISpeechProvider) {
    setDraft({ ...working!, speechProvider: p });
  }
  function setSpeechModel(p: AISpeechProvider, v: string) {
    setDraft({
      ...working!,
      speechModels: { ...working!.speechModels, [p]: v },
    });
  }

  function submit() {
    if (!draft || !data) return;
    const patch: AISettingsUpdate = {};
    if (draft.provider !== data.provider) patch.provider = draft.provider;
    const keyFields: Record<AIProvider, keyof AISettingsUpdate> = {
      openai: "openai_api_key",
      anthropic: "anthropic_api_key",
      gemini: "gemini_api_key",
    };
    const modelFields: Record<AIProvider, keyof AISettingsUpdate> = {
      openai: "openai_model",
      anthropic: "anthropic_model",
      gemini: "gemini_model",
    };
    for (const p of ["openai", "anthropic", "gemini"] as AIProvider[]) {
      if (draft.models[p] !== data.providers[p].model) {
        (patch as Record<string, string>)[modelFields[p]] = draft.models[p];
      }
      if (draft.newKeys[p] !== undefined) {
        (patch as Record<string, string>)[keyFields[p]] = draft.newKeys[p]!;
      }
    }
    // Image settings — only send fields that actually changed.
    const img = data.image ?? { provider: "" as const, ready: false };
    if (draft.imageProvider !== img.provider) {
      patch.image_provider = draft.imageProvider;
    }
    if (draft.imageModels.openai !== (img.openai_model ?? "")) {
      patch.image_openai_model = draft.imageModels.openai;
    }
    if (draft.imageModels.gemini !== (img.gemini_model ?? "")) {
      patch.image_gemini_model = draft.imageModels.gemini;
    }
    // Speech settings — same diff pattern as image.
    const sp = data.speech ?? { provider: "" as const, ready: false };
    if (draft.speechProvider !== sp.provider) {
      patch.speech_provider = draft.speechProvider;
    }
    if (draft.speechModels.openai !== (sp.openai_model ?? "")) {
      patch.speech_openai_model = draft.speechModels.openai;
    }
    if (draft.speechModels.gemini !== (sp.gemini_model ?? "")) {
      patch.speech_gemini_model = draft.speechModels.gemini;
    }
    save.mutate(patch);
  }

  return (
    <div className="space-y-4">
      {isLoading && (
        <Card title="AI providers">
          <Spinner />
        </Card>
      )}
      {error && (
        <ErrorBox
          title="Could not load AI settings"
          detail={
            error instanceof MachineError
              ? `${error.status}: ${JSON.stringify(error.body)}`
              : String(error)
          }
        />
      )}
      {save.error && (
        <ErrorBox
          title="Save failed"
          detail={
            save.error instanceof MachineError
              ? `${save.error.status}: ${JSON.stringify(save.error.body)}`
              : String(save.error)
          }
        />
      )}

      {data && working && (
        <>
          {/* ── Card 1 of 3: provider keys (shared across capabilities) ── */}
          <Card title="AI providers">
            <p className="mb-3 text-xs text-neutral-500">
              Shared API keys for shot analysis and image generation. Keys are
              stored server-side (SQLite, at rest) and never sent back to this
              browser.
            </p>
            <div className="grid gap-3 md:grid-cols-3">
              {PROVIDERS.map((p) => {
                const info = data.providers[p.id];
                const pendingKey = working.newKeys[p.id];
                const cleared = pendingKey === "";
                const hasKey = cleared ? false : info.has_key || !!pendingKey;
                const usage = usageQ.data?.by_provider?.[p.id];
                return (
                  <ProviderKeyTile
                    key={p.id}
                    provider={p}
                    hasStoredKey={info.has_key}
                    hasAnyKey={hasKey}
                    pendingKey={pendingKey}
                    usage={usage}
                    onKeyChange={(v) => setKey(p.id, v)}
                    onKeyClear={() => clearKey(p.id)}
                  />
                );
              })}
            </div>
          </Card>

          {/* ── Card 2 of 3: shot analysis ── */}
          <CapabilityCard
            title="Shot analysis"
            subtitle="Which LLM writes your shot critiques."
            status={
              data.ready
                ? { ready: true, label: data.effective_model ?? "" }
                : { ready: false, label: "no provider ready" }
            }
            providers={PROVIDERS.map((p) => ({
              id: p.id,
              label: p.label,
              available: hasAnyKey(working, data, p.id),
              unavailableReason: "needs key",
            }))}
            selected={working.provider}
            effective={data.effective_provider}
            onProviderChange={setProvider}
            activeProvider={activeAnalysisProvider(working, data)}
            model={
              activeAnalysisProvider(working, data)
                ? working.models[activeAnalysisProvider(working, data)!]
                : ""
            }
            onModelChange={(v) => {
              const p = activeAnalysisProvider(working, data);
              if (p) setModel(p, v);
            }}
            hasStoredKeyForActive={
              activeAnalysisProvider(working, data)
                ? data.providers[activeAnalysisProvider(working, data)!].has_key
                : false
            }
          />

          {/* ── Card 3 of 4: image generation ── */}
          <CapabilityCard
            title="Image generation"
            subtitle="Used by the Profiles page to paint each profile's card."
            status={
              working.imageProvider || data.image?.effective
                ? {
                    ready: !!(data.image?.ready),
                    label: data.image?.effective ?? "—",
                  }
                : { ready: false, label: "needs OpenAI or Gemini key" }
            }
            providers={[
              {
                id: "openai",
                label: "OpenAI",
                available: hasAnyKey(working, data, "openai"),
                unavailableReason: "needs key",
              },
              {
                id: "gemini",
                label: "Google Gemini",
                available: hasAnyKey(working, data, "gemini"),
                unavailableReason: "needs key",
              },
              {
                id: "anthropic",
                label: "Anthropic",
                available: false,
                unavailableReason: "not supported",
                disabledHard: true,
              },
            ]}
            selected={working.imageProvider}
            effective={data.image?.effective}
            onProviderChange={(p) => {
              // Anthropic isn't a valid image provider — guard at the UI
              // layer so we never send it in the patch.
              if (p === "anthropic") return;
              setImageProvider(p as "" | AIImageProvider);
            }}
            activeProvider={activeImageProvider(working, data)}
            model={
              activeImageProvider(working, data) === "openai"
                ? working.imageModels.openai
                : activeImageProvider(working, data) === "gemini"
                ? working.imageModels.gemini
                : ""
            }
            onModelChange={(v) => {
              const p = activeImageProvider(working, data);
              if (p === "openai" || p === "gemini") setImageModel(p, v);
            }}
            modelPlaceholder={
              activeImageProvider(working, data) === "openai"
                ? "gpt-image-1 (default)"
                : activeImageProvider(working, data) === "gemini"
                ? "gemini-2.5-flash-image (default)"
                : ""
            }
            hasStoredKeyForActive={
              activeImageProvider(working, data)
                ? data.providers[activeImageProvider(working, data)!].has_key
                : false
            }
            // Image model lists are effectively a single option each; just
            // show a text input with placeholder for the default.
            freeTextOnly
          />

          {/* ── Card 4 of 4: voice transcription ── */}
          <CapabilityCard
            title="Voice transcription"
            subtitle="Turns the mic button on the shot page into an AI-transcribed note."
            status={
              working.speechProvider || data.speech?.effective
                ? {
                    ready: !!(data.speech?.ready),
                    label: data.speech?.effective ?? "—",
                  }
                : { ready: false, label: "needs OpenAI or Gemini key" }
            }
            providers={[
              {
                id: "openai",
                label: "OpenAI Whisper",
                available: hasAnyKey(working, data, "openai"),
                unavailableReason: "needs key",
              },
              {
                id: "gemini",
                label: "Google Gemini",
                available: hasAnyKey(working, data, "gemini"),
                unavailableReason: "needs key",
              },
              {
                id: "anthropic",
                label: "Anthropic",
                available: false,
                unavailableReason: "no audio API",
                disabledHard: true,
              },
            ]}
            selected={working.speechProvider}
            effective={data.speech?.effective}
            onProviderChange={(p) => {
              if (p === "anthropic") return;
              setSpeechProvider(p as "" | AISpeechProvider);
            }}
            activeProvider={activeSpeechProvider(working, data)}
            model={
              activeSpeechProvider(working, data) === "openai"
                ? working.speechModels.openai
                : activeSpeechProvider(working, data) === "gemini"
                ? working.speechModels.gemini
                : ""
            }
            onModelChange={(v) => {
              const p = activeSpeechProvider(working, data);
              if (p === "openai" || p === "gemini") setSpeechModel(p, v);
            }}
            modelPlaceholder={
              activeSpeechProvider(working, data) === "openai"
                ? "whisper-1 (default)"
                : activeSpeechProvider(working, data) === "gemini"
                ? "gemini-2.5-flash (default)"
                : ""
            }
            hasStoredKeyForActive={
              activeSpeechProvider(working, data)
                ? data.providers[activeSpeechProvider(working, data)!].has_key
                : false
            }
            freeTextOnly
          />

          {/* ── Save bar covers all four cards above ── */}
          <div className="flex items-center justify-end gap-2 pt-1">
            <button
              type="button"
              disabled={!dirty}
              onClick={() => setDraft(null)}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800 disabled:opacity-40"
            >
              Discard
            </button>
            <button
              type="button"
              disabled={!dirty || save.isPending}
              onClick={submit}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
            >
              {save.isPending ? "saving…" : "Save AI settings"}
            </button>
          </div>
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Helpers for resolving which provider the user is *actually* using for
// each capability (i.e. expanding the "Auto" sentinel). Shared between the
// Shot-analysis and Image-generation cards.
// ---------------------------------------------------------------------------

type WorkingDraft = {
  provider: "" | AIProvider;
  models: Record<AIProvider, string>;
  newKeys: Partial<Record<AIProvider, string>>;
  imageProvider: "" | AIImageProvider;
  imageModels: Record<AIImageProvider, string>;
  speechProvider: "" | AISpeechProvider;
  speechModels: Record<AISpeechProvider, string>;
};

function hasAnyKey(working: WorkingDraft, data: AISettings, p: AIProvider): boolean {
  const pending = working.newKeys[p];
  if (pending === "") return false; // user queued a clear
  return data.providers[p].has_key || !!pending;
}

function activeAnalysisProvider(working: WorkingDraft, data: AISettings): AIProvider | null {
  if (working.provider) return working.provider;
  return data.effective_provider ?? null;
}

function activeImageProvider(
  working: WorkingDraft,
  data: AISettings,
): AIImageProvider | null {
  if (working.imageProvider) return working.imageProvider;
  return data.image?.effective ?? null;
}

function activeSpeechProvider(
  working: WorkingDraft,
  data: AISettings,
): AISpeechProvider | null {
  if (working.speechProvider) return working.speechProvider;
  return data.speech?.effective ?? null;
}

// ---------------------------------------------------------------------------
// ProviderKeyTile: one OpenAI/Anthropic/Gemini card in the "AI providers"
// card. Strictly about the API key — the model picker moved to the
// consumer cards (Shot analysis / Image generation) so you don't see three
// copies of the same thing.
//
// API keys are never echoed back from the server; the UI only sees a
// { has_key: boolean }. An empty input leaves the stored key alone; typing
// something new replaces it; clicking "Clear stored key" queues a delete.
// ---------------------------------------------------------------------------
function ProviderKeyTile(props: {
  provider: ProviderLabel;
  hasStoredKey: boolean;
  hasAnyKey: boolean;
  pendingKey: string | undefined;
  usage: AIUsageCostBreak | undefined;
  onKeyChange: (v: string) => void;
  onKeyClear: () => void;
}) {
  const { provider: p, hasStoredKey, hasAnyKey, pendingKey, usage } = props;
  return (
    <div className="space-y-2 rounded-md border border-neutral-800 bg-neutral-950/40 p-3">
      <div className="flex items-center justify-between">
        <div className="text-sm font-medium text-neutral-100">{p.label}</div>
        <span
          className={`rounded-full px-2 py-0.5 text-[11px] ${
            hasAnyKey
              ? "bg-emerald-900/40 text-emerald-300"
              : "bg-neutral-800 text-neutral-500"
          }`}
        >
          {hasAnyKey ? "key set" : "no key"}
        </span>
      </div>

      <input
        type="password"
        autoComplete="off"
        placeholder={hasStoredKey ? "•••••• (stored) — type to replace" : p.placeholder}
        value={pendingKey ?? ""}
        onChange={(e) => props.onKeyChange(e.target.value)}
        className="input font-mono"
      />
      {hasStoredKey && (
        <button
          type="button"
          onClick={props.onKeyClear}
          className="text-[11px] text-neutral-500 hover:text-rose-400"
        >
          Clear stored key
        </button>
      )}

      <ProviderUsageFooter usage={usage} hasKey={hasAnyKey} />
    </div>
  );
}

// ProviderUsageFooter renders the "last 30 days" usage strip under each
// provider tile: calls, total tokens (in+out), and actual $ cost from
// provider-reported token counts. Zero usage shows a muted placeholder
// so the row doesn't collapse and confuse layout.
function ProviderUsageFooter({
  usage,
  hasKey,
}: {
  usage: AIUsageCostBreak | undefined;
  hasKey: boolean;
}) {
  const calls = usage?.calls ?? 0;
  const inTok = usage?.input_tokens ?? 0;
  const outTok = usage?.output_tokens ?? 0;
  const cost = usage?.cost_usd ?? 0;
  const last = usage?.last_used_unix;

  if (!hasKey && calls === 0) {
    return (
      <div className="border-t border-neutral-800 pt-2 text-[11px] text-neutral-600">
        No usage — add a key to start tracking.
      </div>
    );
  }
  if (calls === 0) {
    return (
      <div className="border-t border-neutral-800 pt-2 text-[11px] text-neutral-600">
        Last 30 days: no calls yet.
      </div>
    );
  }
  return (
    <div className="space-y-1 border-t border-neutral-800 pt-2">
      <div className="flex items-center justify-between text-[11px] text-neutral-500">
        <span>Last 30 days</span>
        {last && (
          <span title={new Date(last * 1000).toLocaleString()}>
            {relativeFromNow(last)}
          </span>
        )}
      </div>
      <div className="grid grid-cols-3 gap-1 text-[11px]">
        <div>
          <div className="text-neutral-500">Calls</div>
          <div className="font-mono text-neutral-200">{calls}</div>
        </div>
        <div>
          <div className="text-neutral-500">Tokens</div>
          <div
            className="font-mono text-neutral-200"
            title={`in ${inTok.toLocaleString()} / out ${outTok.toLocaleString()}`}
          >
            {compactNumber(inTok + outTok)}
          </div>
        </div>
        <div>
          <div className="text-neutral-500">Cost</div>
          <div className="font-mono text-emerald-300">
            ${cost < 0.01 ? cost.toFixed(4) : cost.toFixed(2)}
          </div>
        </div>
      </div>
    </div>
  );
}

function compactNumber(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + "k";
  return (n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0) + "M";
}

function relativeFromNow(unix: number): string {
  const diff = Date.now() / 1000 - unix;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

// ---------------------------------------------------------------------------
// CapabilityCard: generic "pick a provider + model" card used by both
// Shot analysis and Image generation. Shows a status chip in the header,
// a row of provider radios (with "needs key" / "not supported" hints
// inline), and a single context-sensitive model picker that changes when
// the provider radio changes. The model picker either uses the provider's
// model-list endpoint (dropdown) or a free-text input (image case).
// ---------------------------------------------------------------------------
type CapabilityProvider = {
  id: AIProvider;
  label: string;
  available: boolean;
  unavailableReason?: string;
  // Hard disabled (e.g. Anthropic for image gen) — can't be flipped by
  // saving a key, so we render it permanently greyed out.
  disabledHard?: boolean;
};

function CapabilityCard(props: {
  title: string;
  subtitle: string;
  status: { ready: boolean; label: string };
  providers: CapabilityProvider[];
  selected: "" | AIProvider;
  effective?: AIProvider;
  onProviderChange: (p: "" | AIProvider) => void;
  activeProvider: AIProvider | null;
  model: string;
  onModelChange: (v: string) => void;
  hasStoredKeyForActive: boolean;
  modelPlaceholder?: string;
  freeTextOnly?: boolean;
}) {
  const {
    title,
    subtitle,
    status,
    providers,
    selected,
    effective,
    onProviderChange,
    activeProvider,
    model,
    onModelChange,
    hasStoredKeyForActive,
    modelPlaceholder,
    freeTextOnly,
  } = props;

  return (
    <Card
      title={title}
      actions={<StatusChip ready={status.ready} label={status.label} />}
    >
      <p className="mb-3 text-xs text-neutral-500">{subtitle}</p>

      <label className="mb-1.5 block text-[11px] uppercase tracking-wide text-neutral-500">
        Provider
      </label>
      <div className="mb-4 flex flex-wrap gap-2">
        <ProviderRadio
          checked={selected === ""}
          onChange={() => onProviderChange("")}
          label="Auto"
          suffix={effective ? `(${effective})` : null}
        />
        {providers.map((p) => (
          <ProviderRadio
            key={p.id}
            checked={selected === p.id}
            onChange={() => onProviderChange(p.id)}
            label={p.label}
            disabled={!p.available}
            disabledHint={p.available ? undefined : p.unavailableReason}
            hardDisabled={p.disabledHard}
          />
        ))}
      </div>

      <label className="mb-1.5 block text-[11px] uppercase tracking-wide text-neutral-500">
        Model{activeProvider && <span className="ml-1 text-neutral-600">· {activeProvider}</span>}
      </label>
      {activeProvider == null ? (
        <div className="text-xs text-neutral-500">
          Pick a provider to choose a model.
        </div>
      ) : freeTextOnly ? (
        <input
          type="text"
          value={model}
          onChange={(e) => onModelChange(e.target.value)}
          placeholder={modelPlaceholder}
          className="input"
        />
      ) : (
        <ModelPicker
          provider={activeProvider}
          hasStoredKey={hasStoredKeyForActive}
          model={model}
          onChange={onModelChange}
        />
      )}
    </Card>
  );
}

function StatusChip({ ready, label }: { ready: boolean; label: string }) {
  return (
    <span
      className={`rounded-full px-2.5 py-0.5 text-[11px] font-medium ${
        ready
          ? "bg-emerald-900/40 text-emerald-300"
          : "bg-amber-900/30 text-amber-300"
      }`}
    >
      <span className="mr-1.5">{ready ? "●" : "○"}</span>
      <span className="font-mono">{label}</span>
    </span>
  );
}

// ModelPicker: dropdown populated from /api/settings/ai/models/{provider}
// when a key is stored. Falls back to a free-text input when there's no
// key or the provider's list endpoint 400s.
function ModelPicker(props: {
  provider: AIProvider;
  hasStoredKey: boolean;
  model: string;
  onChange: (v: string) => void;
}) {
  const { provider, hasStoredKey, model } = props;
  const models = useQuery({
    queryKey: ["ai-models", provider, hasStoredKey],
    queryFn: () => aiModels.list(provider),
    enabled: hasStoredKey,
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
  const list = models.data?.models ?? [];
  const inList = list.includes(model);
  const [customMode, setCustomMode] = useState(false);
  const showCustom = customMode || (hasStoredKey && !inList && list.length > 0 && !model);

  if (!hasStoredKey) {
    return (
      <>
        <input
          type="text"
          value={model}
          onChange={(e) => props.onChange(e.target.value)}
          className="input"
        />
        <p className="mt-1 text-[11px] text-neutral-600">
          Save an API key above to pick from a dropdown.
        </p>
      </>
    );
  }
  if (models.isLoading) {
    return <div className="text-xs text-neutral-500">loading models…</div>;
  }
  if (models.isError || list.length === 0) {
    return (
      <>
        <input
          type="text"
          value={model}
          onChange={(e) => props.onChange(e.target.value)}
          className="input"
        />
        <p className="mt-1 text-[11px] text-amber-400">
          Could not list models ({models.error instanceof Error ? models.error.message : "unknown"}). Free-text fallback.
        </p>
      </>
    );
  }
  if (showCustom) {
    return (
      <>
        <input
          type="text"
          value={model}
          onChange={(e) => props.onChange(e.target.value)}
          className="input"
        />
        <button
          type="button"
          onClick={() => {
            setCustomMode(false);
            if (!list.includes(model) && list.length > 0) {
              props.onChange(list[0]);
            }
          }}
          className="mt-1 text-[11px] text-neutral-500 hover:text-neutral-300"
        >
          ← back to list
        </button>
      </>
    );
  }
  return (
    <>
      <select
        value={model}
        onChange={(e) => {
          if (e.target.value === "__custom__") {
            setCustomMode(true);
          } else {
            props.onChange(e.target.value);
          }
        }}
        className="input"
      >
        {!inList && model && <option value={model}>{model} (current)</option>}
        {list.map((m) => (
          <option key={m} value={m}>
            {m}
          </option>
        ))}
        <option value="__custom__">Custom…</option>
      </select>
      <p className="mt-1 text-[11px] text-neutral-600">
        {list.length} available
      </p>
    </>
  );
}

// ProviderRadio: single pill-style radio used by both CapabilityCards.
// `disabled` means the option is currently unselectable but could become
// available (e.g. "needs key"). `hardDisabled` means it can never be
// enabled (e.g. Anthropic for image gen).
function ProviderRadio({
  checked,
  onChange,
  label,
  suffix,
  disabled,
  disabledHint,
  hardDisabled,
}: {
  checked: boolean;
  onChange: () => void;
  label: string;
  suffix?: string | null;
  disabled?: boolean;
  disabledHint?: string;
  hardDisabled?: boolean;
}) {
  const isDisabled = !!disabled || !!hardDisabled;
  return (
    <label
      title={disabledHint}
      className={`rounded-md border px-3 py-1.5 text-sm ${
        isDisabled
          ? "cursor-not-allowed border-neutral-800 bg-neutral-900/30 text-neutral-600"
          : checked
          ? "cursor-pointer border-emerald-500 bg-emerald-500/10 text-emerald-200"
          : "cursor-pointer border-neutral-700 bg-neutral-900 text-neutral-300 hover:bg-neutral-800"
      }`}
    >
      <input
        type="radio"
        className="sr-only"
        checked={checked}
        disabled={isDisabled}
        onChange={onChange}
      />
      {label}
      {suffix && <span className="ml-1 text-neutral-500">{suffix}</span>}
      {isDisabled && disabledHint && (
        <span className="ml-1.5 text-[10px] text-neutral-600">— {disabledHint}</span>
      )}
    </label>
  );
}
