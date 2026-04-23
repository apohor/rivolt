import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { backend, machine, MachineError, type Profile } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
function newProfileId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `${Date.now().toString(16)}-${Math.random().toString(16).slice(2, 10)}`;
}

// Accepts either the raw profile JSON from Meticulous exports, or a thin
// wrapper like { profile: {...} } / { data: {...} }. Always assigns a fresh
// id so pasting the same export twice won't overwrite the first import.
function normalizeImportedProfile(raw: unknown): Profile {
  if (raw == null || typeof raw !== "object") {
    throw new Error("expected a JSON object");
  }
  let candidate: any = raw;
  if (candidate.profile && typeof candidate.profile === "object") {
    candidate = candidate.profile;
  } else if (candidate.data && typeof candidate.data === "object" && candidate.data.name) {
    candidate = candidate.data;
  }
  if (!candidate.name || typeof candidate.name !== "string") {
    throw new Error("profile JSON is missing a string 'name'");
  }
  const copy: Profile = JSON.parse(JSON.stringify(candidate));
  copy.id = newProfileId();
  return copy;
}

export default function ProfilesPage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: ["profiles"],
    queryFn: () => machine.listProfiles(),
  });
  // Which profiles have an AI-generated image. Used to show the "AI"
  // badge on each list row so you can see at a glance which ones still
  // use the stock machine image. Generate/remove actions live on the
  // per-profile detail page now.
  const aiImages = useQuery({
    queryKey: ["profile-images"],
    queryFn: () => backend.listProfileImages(),
    staleTime: 60_000,
  });
  const aiIdSet = new Set(aiImages.data?.ids ?? []);

  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState("");
  const [parseError, setParseError] = useState<string | null>(null);
  const [nameSuggestion, setNameSuggestion] = useState<{ name: string; reason: string } | null>(null);

  const suggestName = useMutation({
    mutationFn: async () => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(importText);
      } catch (e) {
        throw new Error(`invalid JSON: ${(e as Error).message}`);
      }
      // Unwrap common envelopes so the namer sees the actual profile.
      const candidate: any = (parsed as any)?.profile ?? (parsed as any)?.data ?? parsed;
      const currentName = typeof candidate?.name === "string" ? candidate.name : "";
      return backend.suggestProfileName(candidate, currentName);
    },
    onSuccess: (s) => setNameSuggestion({ name: s.name, reason: s.reason }),
  });

  function applySuggestedName() {
    if (!nameSuggestion || !importText.trim()) return;
    try {
      const parsed = JSON.parse(importText);
      const target: any = parsed?.profile ?? parsed?.data ?? parsed;
      target.name = nameSuggestion.name;
      setImportText(JSON.stringify(parsed, null, 2));
      setNameSuggestion(null);
    } catch {
      // Shouldn't happen — we only enable the button after a successful suggest.
    }
  }

  const importMut = useMutation({
    mutationFn: async (text: string) => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(text);
      } catch (e) {
        throw new Error(`invalid JSON: ${(e as Error).message}`);
      }
      const profile = normalizeImportedProfile(parsed);
      await machine.saveProfile(profile);
      return profile;
    },
    onSuccess: async (profile) => {
      await qc.invalidateQueries({ queryKey: ["profiles"] });
      setImportOpen(false);
      setImportText("");
      setParseError(null);
      navigate(`/profiles/${encodeURIComponent(profile.id)}`);
    },
  });

  function submitImport() {
    setParseError(null);
    if (!importText.trim()) {
      setParseError("Paste a profile JSON first.");
      return;
    }
    importMut.mutate(importText);
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Profiles"
        subtitle={
          data
            ? `${data.length} on the machine · ${aiIdSet.size} with AI image`
            : "Shot profiles saved on the machine"
        }
        actions={
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => {
                setParseError(null);
                setImportOpen((v) => !v);
              }}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800"
            >
              {importOpen ? "Cancel import" : "Import JSON"}
            </button>
            <button
              type="button"
              onClick={() => refetch()}
              disabled={isFetching}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800 disabled:opacity-50"
            >
              {isFetching ? "syncing…" : "Sync now"}
            </button>
          </div>
        }
      />

      {importOpen && (
        <Card title="Import profile from JSON">
          <div className="space-y-3">
            <p className="text-xs text-neutral-400">
              Paste a Meticulous profile JSON (exported from the machine or shared by
              someone else). A fresh id is always assigned, so this won't overwrite
              anything already on the machine.
            </p>
            <textarea
              value={importText}
              onChange={(e) => setImportText(e.target.value)}
              placeholder='{ "name": "My Profile", "temperature": 93, ... }'
              spellCheck={false}
              rows={10}
              className="input font-mono text-xs"
            />
            {parseError && <div className="text-xs text-rose-300">{parseError}</div>}
            {importMut.error && (
              <ErrorBox
                title="Import failed"
                detail={
                  importMut.error instanceof MachineError
                    ? `${importMut.error.status}: ${JSON.stringify(importMut.error.body)}`
                    : String(importMut.error)
                }
              />
            )}
            {suggestName.error && !nameSuggestion && (
              <ErrorBox title="AI suggestion failed" detail={String(suggestName.error)} />
            )}
            {nameSuggestion && (
              <div className="rounded-md border border-sky-900/60 bg-sky-950/30 p-3 text-xs">
                <div className="mb-1 flex items-center gap-2">
                  <span className="uppercase tracking-wide text-sky-300">Suggested name</span>
                </div>
                <div className="text-[15px] font-medium text-neutral-100">
                  {nameSuggestion.name}
                </div>
                {nameSuggestion.reason && (
                  <div className="mt-1 text-neutral-400">{nameSuggestion.reason}</div>
                )}
                <div className="mt-2 flex gap-2">
                  <button
                    type="button"
                    onClick={applySuggestedName}
                    className="rounded-md border border-emerald-800 bg-emerald-900/40 px-2.5 py-1 text-xs text-emerald-200 hover:bg-emerald-900/60"
                  >
                    Apply
                  </button>
                  <button
                    type="button"
                    onClick={() => setNameSuggestion(null)}
                    className="rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1 text-xs hover:bg-neutral-800"
                  >
                    Dismiss
                  </button>
                </div>
              </div>
            )}
            <div className="flex flex-wrap gap-2">
              <button
                type="button"
                disabled={importMut.isPending}
                onClick={submitImport}
                className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
              >
                {importMut.isPending ? "importing…" : "Import & open"}
              </button>
              <button
                type="button"
                disabled={!importText.trim() || suggestName.isPending}
                onClick={() => suggestName.mutate()}
                className="rounded-md border border-sky-800 bg-sky-900/40 px-3 py-1.5 text-sm text-sky-200 hover:bg-sky-900/60 disabled:opacity-40"
              >
                {suggestName.isPending ? "thinking…" : "Suggest name"}
              </button>
              <button
                type="button"
                onClick={() => {
                  setImportOpen(false);
                  setImportText("");
                  setParseError(null);
                }}
                className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800"
              >
                Cancel
              </button>
            </div>
          </div>
        </Card>
      )}

      {isLoading && <Spinner />}
      {error && (
        <ErrorBox
          title="Could not load profiles"
          detail={
            error instanceof MachineError
              ? `${error.status}: ${JSON.stringify(error.body)}`
              : String(error)
          }
        />
      )}
      {data && (
        <Card>
          {data.length === 0 ? (
            <div className="py-8 text-center text-sm text-neutral-500">
              No profiles found.
            </div>
          ) : (
            <ul className="divide-y divide-neutral-800">
              {data.map((p) => {
                const hasAI = aiIdSet.has(p.id);
                const machineImg = machine.profileImageUrl(p.display?.image);
                const img = hasAI ? backend.profileImageSrc(p.id) : machineImg;
                const accent =
                  typeof p.display?.accentColor === "string"
                    ? p.display.accentColor
                    : undefined;
                return (
                  <li key={p.id} className="py-2">
                    <Link
                      to={`/profiles/${encodeURIComponent(p.id)}`}
                      className="-mx-2 flex items-center gap-3 rounded-md px-2 py-1.5 hover:bg-neutral-800/60"
                    >
                      {/* Thumbnail — small and square to match the compact
                          History-row rhythm. Falls back to the accent color
                          if the machine image doesn't load. */}
                      <div
                        className="relative h-12 w-12 shrink-0 overflow-hidden rounded-md bg-neutral-950"
                        style={accent ? { backgroundColor: accent } : undefined}
                      >
                        {img && (
                          <img
                            src={img}
                            alt=""
                            loading="lazy"
                            className="h-full w-full object-cover"
                            onError={(e) => {
                              (e.currentTarget as HTMLImageElement).style.display = "none";
                            }}
                          />
                        )}
                      </div>

                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="truncate text-sm text-neutral-100">
                            {p.name}
                          </span>
                          {hasAI && (
                            <span className="shrink-0 rounded bg-emerald-900/40 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-emerald-300">
                              AI
                            </span>
                          )}
                        </div>
                        <div className="truncate text-xs text-neutral-500">
                          {p.author ?? "—"}
                        </div>
                      </div>

                      <div className="shrink-0 text-right text-xs text-neutral-400">
                        <div className="font-mono">
                          {typeof p.temperature === "number" ? `${p.temperature}°C` : ""}
                          {typeof p.final_weight === "number" ? ` · ${p.final_weight}g` : ""}
                        </div>
                      </div>
                    </Link>
                  </li>
                );
              })}
            </ul>
          )}
        </Card>
      )}
    </div>
  );
}
