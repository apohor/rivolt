import { useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import {
  backend,
  machine,
  MachineError,
  type ExitTrigger,
  type Limit,
  type Profile,
  type Stage,
} from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";

// Deep-clone helper (structuredClone is Safari 15.4+; JSON round-trip works on 15.0).
function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T;
}

export default function ProfileDetailPage() {
  const { id = "" } = useParams();
  const qc = useQueryClient();

  const { data, isLoading, error } = useQuery({
    queryKey: ["profile", id],
    queryFn: () => machine.getProfile(id),
    enabled: !!id,
  });

  // Local draft copy of the profile so edits don't fight the server cache.
  const [draft, setDraft] = useState<Profile | null>(null);
  const working = draft ?? data ?? null;

  const dirty = useMemo(() => {
    if (!draft || !data) return false;
    return JSON.stringify(draft) !== JSON.stringify(data);
  }, [draft, data]);

  const save = useMutation({
    mutationFn: (p: Profile) => machine.saveProfile(p),
    onSuccess: async () => {
      setDraft(null);
      await qc.invalidateQueries({ queryKey: ["profile", id] });
      await qc.invalidateQueries({ queryKey: ["profiles"] });
    },
  });

  function edit(mut: (p: Profile) => void) {
    const base = draft ?? (data ? clone(data) : null);
    if (!base) return;
    const next = clone(base);
    mut(next);
    setDraft(next);
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={working?.name ?? "Profile"}
        subtitle={working?.author ?? undefined}
        actions={
          <div className="flex gap-2">
            <Link
              to="/profiles"
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800"
            >
              Back
            </Link>
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
              onClick={() => working && save.mutate(working)}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
            >
              {save.isPending ? "saving…" : "Save to machine"}
            </button>
          </div>
        }
      />

      {isLoading && <Spinner />}
      {error && (
        <ErrorBox
          title="Could not load profile"
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
      {save.isSuccess && !dirty && (
        <div className="rounded-lg border border-emerald-900 bg-emerald-950/30 px-4 py-2 text-sm text-emerald-200">
          Saved.
        </div>
      )}

      {working && (
        <>
          <ImageCard profile={working} />
          <MetaCard
            profile={working}
            onChangeName={(v) => edit((p) => void (p.name = v))}
            onChangeTemperature={(v) => edit((p) => void (p.temperature = v))}
            onChangeFinalWeight={(v) => edit((p) => void (p.final_weight = v))}
          />
          <VariablesCard
            profile={working}
            onChange={(i, v) =>
              edit((p) => {
                if (!p.variables) return;
                p.variables[i] = { ...p.variables[i], value: v };
              })
            }
          />
          <StagesCard
            profile={working}
            onStageFieldChange={(si, path, v) =>
              edit((p) => {
                if (!p.stages) return;
                setPath(p.stages[si], path, v);
              })
            }
          />
          <JsonCard
            profile={working}
            onReplace={(next) => setDraft(next)}
          />
        </>
      )}
    </div>
  );
}

// --- Cards -----------------------------------------------------------------

// ImageCard shows the profile's artwork — either the AI-generated image
// stored on our server or the stock machine-provided PNG — alongside the
// generate / regenerate / remove controls. Lives on the detail page now
// (instead of being an overlay on every list row) so the Profiles list
// stays compact and the generation UX gets more room to breathe.
function ImageCard({ profile }: { profile: Profile }) {
  const qc = useQueryClient();
  // We only need to know *whether* this specific profile has an AI image;
  // the listProfileImages call is already cached from the list page.
  const aiImages = useQuery({
    queryKey: ["profile-images"],
    queryFn: () => backend.listProfileImages(),
    staleTime: 60_000,
  });
  const hasAI = new Set(aiImages.data?.ids ?? []).has(profile.id);
  const [version, setVersion] = useState(0); // cache-bust after regenerate
  const [genError, setGenError] = useState<string | null>(null);

  const machineImg = machine.profileImageUrl(profile.display?.image);
  const src = hasAI ? backend.profileImageSrc(profile.id, version) : machineImg;
  const accent =
    typeof profile.display?.accentColor === "string"
      ? profile.display.accentColor
      : undefined;

  const generateImg = useMutation({
    mutationFn: () => backend.generateProfileImage(profile.id),
    onMutate: () => setGenError(null),
    onSuccess: async () => {
      setVersion(Date.now());
      await qc.invalidateQueries({ queryKey: ["profile-images"] });
    },
    onError: (err) => {
      if (err instanceof MachineError) {
        const body = err.body as { error?: string; detail?: string } | undefined;
        const detail = body?.detail;
        const short = body?.error;
        // Gemini/OpenAI errors arrive as "<provider> image: http <code>: <json>".
        // Pull out the human-readable message if we can, otherwise show the
        // raw detail so the user has something actionable to search on.
        const pretty = detail ? extractProviderMessage(detail) : undefined;
        setGenError(pretty || detail || short || err.message);
      } else {
        setGenError(String(err));
      }
    },
  });
  const deleteImg = useMutation({
    mutationFn: () => backend.deleteProfileImage(profile.id),
    onSuccess: async () => {
      setVersion(Date.now());
      await qc.invalidateQueries({ queryKey: ["profile-images"] });
    },
  });
  const uploadImg = useMutation({
    mutationFn: (file: File) => backend.uploadProfileImage(profile.id, file),
    onMutate: () => setGenError(null),
    onSuccess: async () => {
      setVersion(Date.now());
      await qc.invalidateQueries({ queryKey: ["profile-images"] });
    },
    onError: (err) => {
      if (err instanceof MachineError) {
        const body = err.body as { error?: string; detail?: string } | undefined;
        setGenError(body?.detail || body?.error || err.message);
      } else {
        setGenError(String(err));
      }
    },
  });
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  return (
    <Card title="Image">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <div
          className="relative aspect-[4/3] w-full overflow-hidden rounded-lg bg-neutral-950 sm:w-72"
          style={accent ? { backgroundColor: accent } : undefined}
        >
          {src ? (
            <img
              src={src}
              alt=""
              className="h-full w-full object-cover"
              onError={(e) => {
                (e.currentTarget as HTMLImageElement).style.display = "none";
              }}
            />
          ) : (
            <div className="flex h-full w-full items-center justify-center text-xs text-neutral-500">
              no image
            </div>
          )}
          {hasAI && (
            <span className="absolute left-2 top-2 rounded bg-black/60 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-emerald-300">
              AI
            </span>
          )}
          {(generateImg.isPending || uploadImg.isPending) && (
            <div className="absolute inset-0 flex items-center justify-center bg-black/60 text-sm text-neutral-200">
              {uploadImg.isPending ? "uploading…" : "generating…"}
            </div>
          )}
        </div>

        <div className="min-w-0 flex-1 space-y-3">
          <p className="text-xs text-neutral-400">
            {hasAI ? (
              <>
                Using a custom image stored on the server. Regenerate or
                upload a new one to replace it, or remove to fall back to
                the stock machine image.
              </>
            ) : (
              <>
                Using the stock image that ships with this profile on the
                machine. Generate an AI image or upload your own to give
                the profile a unique look in the Profiles list.
              </>
            )}
          </p>
          {genError && (
            <div className="rounded-md border border-rose-900 bg-rose-950/40 px-3 py-2 text-xs text-rose-200">
              {genError}
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              disabled={generateImg.isPending || uploadImg.isPending}
              onClick={() => generateImg.mutate()}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
            >
              {generateImg.isPending
                ? "generating…"
                : hasAI
                ? "Regenerate AI image"
                : "Generate AI image"}
            </button>
            <button
              type="button"
              disabled={generateImg.isPending || uploadImg.isPending}
              onClick={() => fileInputRef.current?.click()}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-200 hover:bg-neutral-800 disabled:opacity-40"
            >
              {uploadImg.isPending ? "uploading…" : "Upload image"}
            </button>
            <input
              ref={fileInputRef}
              type="file"
              accept="image/png,image/jpeg,image/webp,image/gif"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) uploadImg.mutate(f);
                e.target.value = "";
              }}
            />
            {hasAI && (
              <button
                type="button"
                disabled={deleteImg.isPending}
                onClick={() => {
                  if (confirm(`Remove the custom image for "${profile.name}"?`)) {
                    deleteImg.mutate();
                  }
                }}
                className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-200 hover:bg-neutral-800 disabled:opacity-40"
              >
                {deleteImg.isPending ? "removing…" : "Remove custom image"}
              </button>
            )}
          </div>
        </div>
      </div>
    </Card>
  );
}

// extractProviderMessage teases a human-readable sentence out of the
// raw provider error string our backend returns in `detail`, e.g.
//   `gemini image: http 503: {"error":{"code":503,"message":"…"}}`
// or `openai image: http 429: {"error":{"message":"…"}}`
// Falls back to returning undefined so callers can render the raw detail.
function extractProviderMessage(detail: string): string | undefined {
  const braceIdx = detail.indexOf("{");
  if (braceIdx === -1) return undefined;
  try {
    const parsed = JSON.parse(detail.slice(braceIdx));
    const msg = parsed?.error?.message;
    if (typeof msg === "string" && msg.length > 0) {
      // Prefix with the "<provider> image: http <code>" preamble so the
      // user sees which service bailed out.
      const preamble = detail.slice(0, braceIdx).replace(/:\s*$/, "").trim();
      return preamble ? `${preamble}: ${msg}` : msg;
    }
  } catch {
    // Fall through.
  }
  return undefined;
}

function MetaCard({
  profile,
  onChangeName,
  onChangeTemperature,
  onChangeFinalWeight,
}: {
  profile: Profile;
  onChangeName: (v: string) => void;
  onChangeTemperature: (v: number) => void;
  onChangeFinalWeight: (v: number) => void;
}) {
  return (
    <Card title="Profile">
      <div className="grid gap-4 md:grid-cols-3">
        <Field label="Name">
          <input
            type="text"
            value={profile.name}
            onChange={(e) => onChangeName(e.target.value)}
            className="input"
          />
        </Field>
        <Field label="Temperature (°C)">
          <NumberInput
            value={profile.temperature}
            step={0.5}
            kind="temperature"
            onChange={(v) => onChangeTemperature(v)}
          />
        </Field>
        <Field label="Final weight (g)">
          <NumberInput
            value={profile.final_weight}
            step={0.5}
            kind="weight"
            onChange={(v) => onChangeFinalWeight(v)}
          />
        </Field>
      </div>
    </Card>
  );
}

function VariablesCard({
  profile,
  onChange,
}: {
  profile: Profile;
  onChange: (index: number, value: number | string) => void;
}) {
  if (!profile.variables || profile.variables.length === 0) return null;
  return (
    <Card title="Variables">
      <div className="grid gap-4 md:grid-cols-2">
        {profile.variables.map((v, i) => (
          <Field key={`${v.key}-${i}`} label={`${v.name} (${v.key})`}>
            {typeof v.value === "number" ? (
              <NumberInput
                value={v.value}
                step={0.1}
                hint={`${v.name} ${v.key}`}
                onChange={(nv) => onChange(i, nv)}
              />
            ) : (
              <input
                type="text"
                value={String(v.value ?? "")}
                onChange={(e) => onChange(i, e.target.value)}
                className="input"
              />
            )}
          </Field>
        ))}
      </div>
    </Card>
  );
}

function StagesCard({
  profile,
  onStageFieldChange,
}: {
  profile: Profile;
  onStageFieldChange: (stageIndex: number, path: (string | number)[], value: unknown) => void;
}) {
  if (!profile.stages || profile.stages.length === 0) {
    return (
      <Card title="Stages">
        <div className="text-sm text-neutral-500">This profile has no stages.</div>
      </Card>
    );
  }
  return (
    <Card title={`Stages (${profile.stages.length})`}>
      <ol className="space-y-3">
        {profile.stages.map((s, i) => (
          <li key={i} className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3">
            <StageBlock
              stage={s}
              onFieldChange={(path, v) => onStageFieldChange(i, path, v)}
            />
          </li>
        ))}
      </ol>
    </Card>
  );
}

function StageBlock({
  stage,
  onFieldChange,
}: {
  stage: Stage;
  onFieldChange: (path: (string | number)[], value: unknown) => void;
}) {
  return (
    <div className="space-y-3">
      <header className="flex items-center justify-between">
        <div>
          <span className="text-sm font-medium text-neutral-100">{stage.name}</span>
          <span className="ml-2 rounded bg-neutral-800 px-1.5 py-0.5 text-xs text-neutral-300">
            {stage.type}
          </span>
        </div>
      </header>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label="Name">
          <input
            type="text"
            value={stage.name}
            onChange={(e) => onFieldChange(["name"], e.target.value)}
            className="input"
          />
        </Field>
        {collectNumericDynamics(stage).map(({ key, value }) => (
          <Field key={`d-${key}`} label={`dynamics.${key}`}>
            <NumberInput
              value={value}
              step={0.1}
              hint={key}
              onChange={(v) => onFieldChange(["dynamics", key], v)}
            />
          </Field>
        ))}
        {(stage.exit_triggers ?? []).map((t: ExitTrigger, idx: number) => (
          <Field key={`et-${idx}`} label={`exit · ${t.type}`}>
            <NumberInput
              value={t.value}
              step={0.1}
              hint={t.type}
              onChange={(v) => onFieldChange(["exit_triggers", idx, "value"], v)}
            />
          </Field>
        ))}
        {(stage.limits ?? []).map((l: Limit, idx: number) => (
          <Field key={`lim-${idx}`} label={`limit · ${l.type}`}>
            <NumberInput
              value={l.value}
              step={0.1}
              hint={l.type}
              onChange={(v) => onFieldChange(["limits", idx, "value"], v)}
            />
          </Field>
        ))}
      </div>
    </div>
  );
}

// --- Primitives ------------------------------------------------------------

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="block text-xs uppercase tracking-wide text-neutral-500">{label}</span>
      {children}
    </label>
  );
}

// Infer a reasonable slider range from a domain hint. Returns null when we
// don't know the domain — caller then falls back to a plain number input.
// Ranges are deliberately a little generous (e.g. pressure goes to 12 bar
// even though a Meticulous maxes out at 10) to avoid clamping user intent.
function rangeFor(
  kind: string | undefined,
): { min: number; max: number; step: number } | null {
  switch (kind) {
    case "temperature":
      return { min: 80, max: 105, step: 0.5 };
    case "weight":
      return { min: 0, max: 100, step: 0.5 };
    case "pressure":
      return { min: 0, max: 12, step: 0.1 };
    case "flow":
      return { min: 0, max: 15, step: 0.1 };
    case "time":
      return { min: 0, max: 180, step: 0.5 };
    case "position":
    case "piston_position":
      return { min: 0, max: 80, step: 0.5 };
    case "power":
      return { min: 0, max: 100, step: 1 };
    case "speed":
      return { min: 0, max: 10, step: 0.1 };
    case "percent":
    case "ratio":
      return { min: 0, max: 100, step: 1 };
    default:
      return null;
  }
}

// Guess the kind from an arbitrary key/label string. Matches substrings so
// "dynamics.pressure" or "exit · flow" both resolve. Order matters —
// "piston_position" must beat the generic "position" check (both map the
// same anyway).
function guessKind(hint: string | undefined): string | undefined {
  if (!hint) return undefined;
  const h = hint.toLowerCase();
  if (h.includes("temperature") || h.includes("temp")) return "temperature";
  if (h.includes("weight")) return "weight";
  if (h.includes("pressure")) return "pressure";
  if (h.includes("flow")) return "flow";
  if (h.includes("time")) return "time";
  if (h.includes("position")) return "position";
  if (h.includes("power")) return "power";
  if (h.includes("speed")) return "speed";
  if (h.includes("percent") || h.includes("ratio")) return "percent";
  return undefined;
}

function NumberInput({
  value,
  step,
  onChange,
  kind,
  hint,
}: {
  value: number | undefined;
  step?: number;
  onChange: (v: number) => void;
  /** Explicit domain — takes precedence over hint. */
  kind?: string;
  /** Free-form label used to guess the domain when kind isn't set. */
  hint?: string;
}) {
  const range = rangeFor(kind ?? guessKind(hint));
  const sliderStep = step ?? range?.step ?? 0.1;

  // No known range → plain number input (no slider noise for unknown fields).
  if (!range) {
    return (
      <input
        type="number"
        inputMode="decimal"
        step={step ?? "any"}
        value={value ?? ""}
        onChange={(e) => {
          const v = e.target.value;
          if (v === "") return;
          const n = Number(v);
          if (!Number.isNaN(n)) onChange(n);
        }}
        className="input"
      />
    );
  }

  // Clamp the slider thumb into range, but let the number input accept
  // anything — users may legitimately need out-of-range values and we'd
  // rather not silently clobber them.
  const sliderValue =
    value === undefined ? range.min : Math.min(range.max, Math.max(range.min, value));

  return (
    <div className="flex items-center gap-3">
      <input
        type="range"
        min={range.min}
        max={range.max}
        step={sliderStep}
        value={sliderValue}
        onChange={(e) => onChange(Number(e.target.value))}
        className="caffeine-range flex-1"
      />
      <input
        type="number"
        inputMode="decimal"
        step={step ?? "any"}
        value={value ?? ""}
        onChange={(e) => {
          const v = e.target.value;
          if (v === "") return;
          const n = Number(v);
          if (!Number.isNaN(n)) onChange(n);
        }}
        className="input w-20 shrink-0"
      />
    </div>
  );
}

// --- helpers ---------------------------------------------------------------

function collectNumericDynamics(stage: Stage): { key: string; value: number }[] {
  const dyn = stage.dynamics;
  if (!dyn || typeof dyn !== "object") return [];
  return Object.entries(dyn)
    .filter(([, v]) => typeof v === "number")
    .map(([key, value]) => ({ key, value: value as number }));
}

function setPath(obj: unknown, path: (string | number)[], value: unknown): void {
  let cursor: any = obj;
  for (let i = 0; i < path.length - 1; i++) {
    const k = path[i];
    if (cursor[k] === undefined || cursor[k] === null) {
      cursor[k] = typeof path[i + 1] === "number" ? [] : {};
    }
    cursor = cursor[k];
  }
  cursor[path[path.length - 1]] = value;
}

// --- JsonCard --------------------------------------------------------------
// Raw JSON escape hatch. Useful for bulk tweaks, pasting a profile from
// somewhere else, or rescuing a profile the structured editor can't express.
function JsonCard({
  profile,
  onReplace,
}: {
  profile: Profile;
  onReplace: (next: Profile) => void;
}) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState<string>("");
  const [err, setErr] = useState<string | null>(null);

  function enterEdit() {
    setText(JSON.stringify(profile, null, 2));
    setErr(null);
    setOpen(true);
  }

  function apply() {
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      return;
    }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      setErr("JSON root must be an object");
      return;
    }
    const next = parsed as Profile;
    if (next.id && next.id !== profile.id) {
      setErr(`id mismatch: JSON has "${next.id}", expected "${profile.id}"`);
      return;
    }
    // Preserve id so users can't accidentally remap which profile they're
    // editing by deleting or retyping the id.
    next.id = profile.id;
    onReplace(next);
    setOpen(false);
  }

  return (
    <section className="rounded-xl border border-neutral-800 bg-neutral-950/60 p-4">
      <div className="flex items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold">Raw JSON</h3>
          <p className="text-xs text-neutral-400">
            Edit the full profile document. Changes stage as a draft — you
            still need to hit <span className="text-neutral-200">Save to machine</span>.
          </p>
        </div>
        {!open && (
          <button
            type="button"
            onClick={enterEdit}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-200 hover:bg-neutral-800"
          >
            Edit JSON
          </button>
        )}
      </div>

      {open && (
        <div className="mt-3 space-y-2">
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            spellCheck={false}
            className="h-96 w-full rounded-md border border-neutral-800 bg-neutral-900 p-3 font-mono text-xs text-neutral-100 focus:border-neutral-600 focus:outline-none"
          />
          {err && (
            <div className="rounded-md border border-red-900 bg-red-950/30 px-3 py-2 text-xs text-red-200">
              {err}
            </div>
          )}
          <div className="flex gap-2">
            <button
              type="button"
              onClick={apply}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500"
            >
              Apply as draft
            </button>
            <button
              type="button"
              onClick={() => setOpen(false)}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-200 hover:bg-neutral-800"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => setText(JSON.stringify(profile, null, 2))}
              className="ml-auto rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800"
            >
              Reset to current
            </button>
          </div>
        </div>
      )}
    </section>
  );
}
