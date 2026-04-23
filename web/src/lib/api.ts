// API helpers for the machine proxy.
//
// Routes mounted on the Go backend:
//   /api/health                → { status, time }
//   /api/config                → { machine_url }
//   /api/machine-status        → { reachable, machine_url, ... }
//   /api/machine/*             → reverse-proxy to ${machine_url}/api/*
//
// Example: `GET /api/machine/v1/settings` → `${machine}/api/v1/settings`.

export class MachineError extends Error {
  status: number;
  body: unknown;
  constructor(status: number, body: unknown, msg?: string) {
    // Prefer the server's own error message when present — most of our
    // handlers respond with { "error": "..." } and that's much more useful
    // than a bare "machine 502" (which is doubly wrong when the failing
    // endpoint isn't the machine proxy at all, e.g. AI analysis).
    let message = msg;
    if (!message && body && typeof body === "object" && "error" in body) {
      const e = (body as { error?: unknown }).error;
      if (typeof e === "string" && e.length > 0) message = e;
    }
    super(message ?? `HTTP ${status}`);
    this.status = status;
    this.body = body;
  }
}

async function request<T>(
  method: string,
  url: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  });
  const text = await res.text();
  let parsed: unknown = undefined;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = text;
    }
  }
  if (!res.ok) throw new MachineError(res.status, parsed);
  return parsed as T;
}

export const api = {
  get: <T>(url: string, signal?: AbortSignal) => request<T>("GET", url, undefined, signal),
  post: <T>(url: string, body?: unknown, signal?: AbortSignal) =>
    request<T>("POST", url, body, signal),
  put: <T>(url: string, body?: unknown, signal?: AbortSignal) =>
    request<T>("PUT", url, body, signal),
  del: <T>(url: string, signal?: AbortSignal) => request<T>("DELETE", url, undefined, signal),
};

// Machine endpoints are prefixed with /api/machine/v1/…
export const machine = {
  listProfiles: () => api.get<ProfileListItem[]>("/api/machine/v1/profile/list"),
  getProfile: (id: string) =>
    api.get<Profile>(`/api/machine/v1/profile/get/${encodeURIComponent(id)}`),
  saveProfile: (p: Profile) => api.post<SaveProfileResponse>("/api/machine/v1/profile/save", p),
  getSettings: () => api.get<MachineSettings>("/api/machine/v1/settings"),
  // Machine accepts { key: value } single-key patches.
  updateSetting: (key: string, value: unknown) =>
    api.post<MachineSettings>("/api/machine/v1/settings", { [key]: value }),
  // Machine actions are GET /api/v1/action/{name}
  // (start | stop | continue | reset | tare | preheat | purge | home |
  // calibration | scale_master_calibration).
  triggerAction: (name: string) =>
    api.get<{ action: string; status: string }>(
      `/api/machine/v1/action/${encodeURIComponent(name)}`,
    ),
  // GET /api/v1/profile/load/{id} tells the machine to make this
  // profile the active one. The machine's Socket.io status stream will
  // then start reporting it as `loaded_profile`, so the Live page can
  // react without a separate round-trip.
  loadProfile: (id: string) =>
    api.get<{ id: string; status?: string }>(
      `/api/machine/v1/profile/load/${encodeURIComponent(id)}`,
    ),
  // Rewrites a machine-relative image path (e.g. "/api/v1/profile/image/<hash>.png")
  // so it goes through our reverse proxy. Returns undefined if the input is
  // missing so callers can skip rendering.
  profileImageUrl: (path?: string | null): string | undefined => {
    if (!path) return undefined;
    if (/^https?:\/\//i.test(path)) return path;
    if (path.startsWith("/api/v1/")) return `/api/machine${path.slice(4)}`;
    if (path.startsWith("/v1/")) return `/api/machine${path}`;
    return path;
  },
};

export const backend = {
  machineStatus: () => api.get<MachineStatus>("/api/machine-status"),
  config: () => api.get<{ machine_url: string }>("/api/config"),
  health: () => api.get<{ status: string; time: string }>("/api/health"),
  liveStatus: () => api.get<LiveHubStatus>("/api/live/status"),
  listShots: (limit = 100) => api.get<ShotListItem[]>(`/api/shots?limit=${limit}`),
  listShotSparklines: (limit = 200, points = 24) =>
    api.get<Record<string, number[]>>(
      `/api/shots/sparklines?limit=${limit}&points=${points}`,
    ),
  // Richer cousin of listShotSparklines — same sparkline trace plus
  // peak pressure and final weight so the history row can show them
  // without pulling the whole shot payload.
  listShotMetrics: (limit = 200, points = 24) =>
    api.get<Record<string, ShotMetrics>>(
      `/api/shots/metrics?limit=${limit}&points=${points}`,
    ),
  getShot: (id: string) => api.get<Shot>(`/api/shots/${encodeURIComponent(id)}`),
  deleteShot: (id: string) =>
    api.del<void>(`/api/shots/${encodeURIComponent(id)}`),
  setShotFeedback: (id: string, rating: number | null, note: string) =>
    api.put<{ ok: boolean; rating: number | null; note: string }>(
      `/api/shots/${encodeURIComponent(id)}/feedback`,
      { rating, note },
    ),
  // Send a raw audio blob to the backend for AI transcription.
  // The server uses the provider configured in Settings → Voice
  // transcription; passing `provider` lets callers override it for a
  // single request (useful for testing).
  transcribe: async (audio: Blob, provider?: AISpeechProvider) => {
    const url = provider
      ? `/api/ai/transcribe?provider=${encodeURIComponent(provider)}`
      : "/api/ai/transcribe";
    const res = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": audio.type || "audio/webm" },
      body: audio,
    });
    const text = await res.text();
    let parsed: unknown = undefined;
    if (text) {
      try { parsed = JSON.parse(text); } catch { parsed = text; }
    }
    if (!res.ok) throw new MachineError(res.status, parsed);
    return parsed as { text: string; language?: string; provider?: string };
  },
  // Scan a coffee-bag photo and extract bean fields. Uses OpenAI vision
  // server-side; the image never hits disk. Caller feeds the result
  // into the Beans form for the user to review before saving.
  extractBeanFromImage: async (file: Blob) => {
    const form = new FormData();
    form.append("image", file, (file as File).name || "bag.jpg");
    const res = await fetch("/api/ai/beans/from-image", {
      method: "POST",
      body: form,
    });
    const text = await res.text();
    let parsed: unknown = undefined;
    if (text) {
      try { parsed = JSON.parse(text); } catch { parsed = text; }
    }
    if (!res.ok) throw new MachineError(res.status, parsed);
    return parsed as ExtractedBean;
  },
  shotsStatus: () => api.get<ShotsStatus>("/api/shots/status"),
  syncShots: () => api.post<ShotsStatus>("/api/shots/sync"),
  getAnalysis: (id: string) =>
    api.get<ShotAnalysis>(`/api/shots/${encodeURIComponent(id)}/analysis`),
  createAnalysis: (id: string) =>
    api.post<ShotAnalysis>(`/api/shots/${encodeURIComponent(id)}/analysis`),
  // Profile coach: one focused suggestion for the next pull, informed
  // by sibling shots on the same profile.
  coachShot: (id: string) =>
    api.post<CoachSuggestion>(`/api/shots/${encodeURIComponent(id)}/coach`),
  // Cached coach suggestion. Returns 404 if the shot has never been
  // coached; the caller should render an empty-state CTA.
  getCoachSuggestion: (id: string) =>
    api.get<CoachSuggestion>(`/api/shots/${encodeURIComponent(id)}/coach`),
  // Shot-to-shot comparison: returns a markdown report.
  compareShots: (a: string, b: string) =>
    api.post<ShotComparison>(`/api/shots/compare`, { a, b }),
  // Cached comparison for a pair of shots. a/b may be passed in either
  // order — the backend canonicalises. 404 when the pair has never been
  // compared.
  getComparison: (a: string, b: string) =>
    api.get<ShotComparison>(
      `/api/shots/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}`,
    ),
  // Profile-name suggestion: ask the LLM to pick a short human-friendly
  // name for a pasted profile JSON.
  suggestProfileName: (profile: unknown, currentName = "") =>
    api.post<ProfileNameSuggestion>(`/api/ai/profile-name`, {
      profile,
      current_name: currentName,
    }),
  getAISettings: () => api.get<AISettings>("/api/settings/ai"),
  updateAISettings: (patch: AISettingsUpdate) => api.put<AISettings>("/api/settings/ai", patch),
  // AI usage ledger — feeds the Settings dashboard with a rollup of LLM
  // calls the app has made (estimated cost, calls by provider/feature,
  // recent entries). Defaults: last 30 days, most recent 50 calls.
  getAIUsage: (days = 30, recent = 50) =>
    api.get<AIUsageSummary>(
      `/api/ai/usage?days=${encodeURIComponent(String(days))}&recent=${encodeURIComponent(
        String(recent),
      )}`,
    ),
  // Beans CRUD — the user's bag-of-beans inventory, linked to shots.
  listBeans: () => api.get<Bean[]>("/api/beans"),
  createBean: (input: BeanInput) => api.post<Bean>("/api/beans", input),
  updateBean: (id: string, input: BeanInput) =>
    api.put<Bean>(`/api/beans/${encodeURIComponent(id)}`, input),
  deleteBean: (id: string) => api.del<void>(`/api/beans/${encodeURIComponent(id)}`),
  setShotBean: (shotId: string, beanId: string) =>
    api.put<void>(`/api/shots/${encodeURIComponent(shotId)}/bean`, { bean_id: beanId }),
  // Mark one bean as the "bag currently in use" — new shots get
  // auto-tagged with it until cleared.
  setBeanActive: (id: string) =>
    api.put<void>(`/api/beans/${encodeURIComponent(id)}/active`, {}),
  clearBeanActive: () => api.del<void>("/api/beans/active"),
  // Per-shot grinder setting label and optional RPM.
  setShotGrind: (shotId: string, grind: string, rpm: number | null) =>
    api.put<void>(`/api/shots/${encodeURIComponent(shotId)}/grind`, {
      grind,
      rpm,
    }),
  listPreheatSchedules: () => api.get<PreheatSchedule[]>("/api/preheat/schedules"),
  createPreheatSchedule: (sch: PreheatScheduleInput) =>
    api.post<PreheatSchedule>("/api/preheat/schedules", sch),
  updatePreheatSchedule: (id: string, sch: PreheatScheduleInput) =>
    api.put<PreheatSchedule>(`/api/preheat/schedules/${encodeURIComponent(id)}`, sch),
  deletePreheatSchedule: (id: string) =>
    api.del<void>(`/api/preheat/schedules/${encodeURIComponent(id)}`),
  preheatStatus: () => api.get<PreheatStatus>("/api/preheat/status"),
  triggerPreheat: () => api.post<PreheatStatus>("/api/preheat/now"),

  // AI-generated profile artwork. Images are cached server-side by profile
  // id; the list endpoint lets the Profiles grid show an "AI" badge without
  // hitting one URL per card.
  listProfileImages: () =>
    api.get<{ ids: string[] }>("/api/profiles/images"),
  generateProfileImage: (id: string, prompt?: string) =>
    api.post<{
      url: string;
      bytes: number;
      mime: string;
      model: string;
      prompt: string;
    }>(`/api/profiles/${encodeURIComponent(id)}/image/generate`,
       prompt ? { prompt } : {}),
  deleteProfileImage: (id: string) =>
    api.del<void>(`/api/profiles/${encodeURIComponent(id)}/image`),
  // Upload a user-supplied image as the profile's artwork. We send the
  // raw file bytes with Content-Type set to the file's mime so the
  // backend can sniff if the browser lied.
  uploadProfileImage: async (id: string, file: File) => {
    const res = await fetch(`/api/profiles/${encodeURIComponent(id)}/image`, {
      method: "PUT",
      headers: { "Content-Type": file.type || "application/octet-stream" },
      body: file,
    });
    const text = await res.text();
    let parsed: unknown = undefined;
    if (text) {
      try { parsed = JSON.parse(text); } catch { parsed = text; }
    }
    if (!res.ok) throw new MachineError(res.status, parsed);
    return parsed as { url: string; bytes: number; mime: string };
  },
  // URL for the currently-stored AI image. `v` lets callers bust the
  // browser cache after a regenerate.
  profileImageSrc: (id: string, v?: number | string) => {
    const suffix = v != null ? `?v=${encodeURIComponent(String(v))}` : "";
    return `/api/profiles/${encodeURIComponent(id)}/image${suffix}`;
  },
};

// --- Types -----------------------------------------------------------------

export type MachineStatus = {
  reachable: boolean;
  machine_url: string;
  status_code?: number;
  error?: string;
  degraded?: boolean;
  last_seen_unix?: number;
  attempts?: number;
};

// LiveHubStatus mirrors live.State on the backend: whether our upstream
// WebSocket to the machine is currently connected.
export type LiveHubStatus = {
  connected: boolean;
  last_connect: string;
  last_error?: string;
  machine_url: string;
};

export type ProfileListItem = {
  id: string;
  name: string;
  author?: string;
  temperature?: number;
  final_weight?: number;
  last_changed?: number;
  display?: { image?: string; accentColor?: string; [k: string]: unknown };
  [k: string]: unknown;
};

// The machine's profile shape is nested and evolving; we keep it loose
// (unknown) at the TypeScript boundary and narrow in individual components.
export type Profile = {
  id: string;
  author?: string;
  name: string;
  temperature?: number;
  final_weight?: number;
  display?: { image?: string; [k: string]: unknown };
  variables?: Variable[];
  stages?: Stage[];
  [k: string]: unknown;
};

export type Variable = {
  name: string;
  key: string;
  type: string;
  value: number | string;
  [k: string]: unknown;
};

export type Stage = {
  name: string;
  key?: string;
  type: string;
  dynamics?: Record<string, unknown>;
  exit_triggers?: ExitTrigger[];
  limits?: Limit[];
  [k: string]: unknown;
};

export type ExitTrigger = {
  type: string;
  value: number;
  [k: string]: unknown;
};

export type Limit = {
  type: string;
  value: number;
  [k: string]: unknown;
};

export type SaveProfileResponse = { change: string; profile?: Profile; [k: string]: unknown };

export type MachineSettings = Record<string, unknown>;

// --- Shots ----------------------------------------------------------------

export type ShotListItem = {
  id: string;
  time: number;
  name: string;
  file?: string;
  profile_id?: string;
  profile_name?: string;
  sample_count: number;
  rating?: number; // 1..5, omitted when unrated
  note?: string;
  bean_id?: string; // links to Bean.id (empty when unset)
  // Grinder setting label (free-form: "2.8", "12 clicks") and RPM
  // for variable-speed grinders. Either may be absent.
  grind?: string;
  grind_rpm?: number;
};

// Per-shot summary used to decorate the history/home list rows. The
// sparkline is a downsampled pressure trace; peak_pressure (bar) and
// final_weight (g) are extracted from the full samples blob on the
// server so we don't have to ship the whole thing to the browser.
export type ShotMetrics = {
  spark?: number[];
  peak_pressure?: number;
  final_weight?: number;
};

// ShotSample is deliberately loose. Known fields at the time of writing:
//   time: number (seconds since shot start)
//   profile_time: number
//   status: string
//   shot: { pressure, flow, weight, gravimetric_flow, setpoints: {...} }
//   sensors: { external_1, external_2, bar_up, bar_mid_up, ... }
export type ShotSample = {
  time: number;
  profile_time?: number;
  status?: string;
  shot?: {
    pressure?: number;
    flow?: number;
    weight?: number;
    gravimetric_flow?: number;
    setpoints?: Record<string, unknown>;
    [k: string]: unknown;
  };
  sensors?: Record<string, number>;
  [k: string]: unknown;
};

export type Shot = ShotListItem & {
  debug_file?: string;
  samples: ShotSample[];
  profile?: unknown;
};

export type ShotsStatus = {
  last_sync: string;
  last_error?: string;
  shots_cached: number;
  machine_url: string;
  interval_secs: number;
  enabled?: boolean;
};

export type ShotAnalysis = {
  model: string;
  created_at: string;
  /**
   * High-level grade parsed from the LLM's first line (e.g.
   * "RATING: 7/10 good"). May be absent if the model didn't emit it.
   */
  rating?: {
    score: number; // 0..10
    label?: string; // excellent | good | fine | off | bad
  };
  summary: string;
  metrics: {
    duration_s: number;
    preinfusion_end_s?: number;
    peak_pressure_bar: number;
    avg_pressure_bar: number;
    peak_flow_mls: number;
    avg_flow_mls: number;
    final_weight_g: number;
    first_drip_s?: number;
  };
};

// Profile coach: a single focused recipe suggestion.
export type CoachSuggestion = {
  model: string;
  created_at: string;
  change: string;
  rationale: string;
  var_key?: string;
  before?: number | null;
  after?: number | null;
  confidence: "low" | "medium" | "high";
};

// Shot-to-shot comparison result — markdown rendered verbatim.
export type ShotComparison = {
  model: string;
  created_at: string;
  markdown: string;
};

// Profile-name suggestion from the LLM.
export type ProfileNameSuggestion = {
  model: string;
  created_at: string;
  name: string;
  reason: string;
};

// Beans inventory.
export type Bean = {
  id: string;
  name: string;
  roaster?: string;
  origin?: string;
  process?: string;
  roast_level?: string;
  roast_date?: string; // YYYY-MM-DD
  notes?: string;
  created_at_unix: number;
  archived?: boolean;
  // Active marks the "bag currently in use" — new shots get
  // auto-tagged with this bean id. At most one bean has active=true.
  active?: boolean;
  // Default grinder settings for this bag, used as the starting
  // point when the user pulls a new shot with it loaded. Either
  // field can be omitted (the user just leaves it blank on the
  // shot until they dial it in).
  default_grind_size?: string;
  default_grind_rpm?: number | null;
};

export type BeanInput = {
  name: string;
  roaster?: string;
  origin?: string;
  process?: string;
  roast_level?: string;
  roast_date?: string;
  notes?: string;
  archived?: boolean;
  default_grind_size?: string;
  default_grind_rpm?: number | null;
};

// ExtractedBean is what the vision endpoint returns after scanning a
// coffee-bag photo. Every field is optional (the LLM leaves anything
// it couldn't read as an empty string); confidence is a 0..1 hint the
// UI can surface when the read was dodgy.
export type ExtractedBean = {
  name: string;
  roaster: string;
  origin: string;
  process: string;
  roast_level: string;
  roast_date: string;
  notes: string;
  confidence: number;
};

// --- App-level AI settings ------------------------------------------------

export type AIProvider = "openai" | "anthropic" | "gemini";

// Providers that can actually generate images. Anthropic is deliberately
// excluded — Claude can see images (vision) but the API has no image
// generation endpoint. The UI shows it as "not supported".
export type AIImageProvider = "openai" | "gemini";

// Providers that can transcribe audio. Anthropic is excluded for the same
// reason as images — Claude has no audio-input API (yet).
export type AISpeechProvider = "openai" | "gemini";

export type AISettings = {
  // "" means auto (first provider with a key wins).
  provider: "" | AIProvider;
  effective_provider?: AIProvider;
  // e.g. "openai:gpt-4o-mini" — set only when ready=true.
  effective_model?: string;
  providers: Record<AIProvider, { model: string; has_key: boolean }>;
  ready: boolean;
  // Image generation has its own provider + per-provider model overrides.
  // The keys are shared with `providers` above. Optional so the UI keeps
  // working against an older backend that hasn't been redeployed yet.
  image?: {
    provider: "" | AIImageProvider;
    effective?: AIImageProvider;
    openai_model?: string;
    gemini_model?: string;
    ready: boolean;
  };
  // Voice transcription pipeline. Same shape as image.
  speech?: {
    provider: "" | AISpeechProvider;
    effective?: AISpeechProvider;
    openai_model?: string;
    gemini_model?: string;
    ready: boolean;
  };
};

// Any field may be omitted (server treats nil as "leave alone"). An explicit
// empty string clears a value.
export type AISettingsUpdate = {
  provider?: "" | AIProvider;
  openai_model?: string;
  openai_api_key?: string;
  anthropic_model?: string;
  anthropic_api_key?: string;
  gemini_model?: string;
  gemini_api_key?: string;
  image_provider?: "" | AIImageProvider;
  image_openai_model?: string;
  image_gemini_model?: string;
  speech_provider?: "" | AISpeechProvider;
  speech_openai_model?: string;
  speech_gemini_model?: string;
};

// Model catalogue fetched from the provider's own list endpoint. We pipe it
// through the backend so the API key never leaves the server.
export const aiModels = {
  list: (provider: AIProvider) =>
    api.get<{ models: string[] }>(`/api/settings/ai/models/${provider}`),
};

// Rollup of AI usage served by /api/ai/usage. Cost is the real USD charge
// computed from provider-reported input/output token counts × the
// published per-1M-token prices — no chars, no estimates.
export type AIUsageCostBreak = {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  last_used_unix?: number;
};
export type AIUsageRecord = {
  Time: string;
  Provider: string;
  Model: string;
  Feature: string;
  InputTokens: number;
  OutputTokens: number;
  DurationMs: number;
  ShotID?: string;
  OK: boolean;
  Err?: string;
};
export type AIUsageSummary = {
  since: string;
  total_calls: number;
  total_cost_usd: number;
  by_provider: Record<string, AIUsageCostBreak>;
  by_feature: Record<string, AIUsageCostBreak>;
  by_model: Record<string, AIUsageCostBreak>;
  recent: AIUsageRecord[];
};

// --- Preheat schedules ----------------------------------------------------

// Bitmask: Sun=bit0, Mon=bit1, ... Sat=bit6. e.g. 0b0111110 = 62 = Mon-Fri.
export type PreheatSchedule = {
  id: string;
  name: string;
  enabled: boolean;
  time_of_day: string; // "HH:MM" 24h
  weekday_mask: number;
  created_at: string;
  updated_at: string;
};

export type PreheatScheduleInput = {
  name: string;
  enabled: boolean;
  time_of_day: string;
  weekday_mask: number;
};

export type PreheatStatus = {
  last_triggered?: string;
  last_source?: string;
  last_error?: string;
  next_scheduled?: string;
  next_schedule?: string;
  timezone?: string; // IANA name or "UTC"; HH:MM schedules are interpreted here
};
