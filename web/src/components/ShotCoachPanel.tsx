// Profile coach panel: one focused next-pull suggestion from the LLM.
//
// Distinct from ShotAnalysisPanel (full critique with rating + sections).
// The coach produces a single structured change you can act on tomorrow,
// informed by recent shots on the same profile. Mirrors the analysis
// panel's read-through pattern: on mount we GET any cached suggestion
// so the user sees their previous coach result without another LLM
// charge; "Re-run" replaces it with a fresh one.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, MachineError, type CoachSuggestion } from "../lib/api";
import { Card, ErrorBox } from "./ui";
import ApplyProfileChange from "./ApplyProfileChange";

type Props = {
  shotId: string;
  /** Profile id the shot was pulled with. When the coach proposes a
   *  profile-variable change we show an "Apply" button that writes it
   *  straight to the machine. Omit for live/unsynced shots. */
  profileId?: string;
};

export default function ShotCoachPanel({ shotId, profileId }: Props) {
  const qc = useQueryClient();

  // Pull any cached coach suggestion so the user sees their previous
  // result immediately. 404 is the expected "not coached yet" state —
  // swallow it and fall through to the CTA.
  const cached = useQuery({
    queryKey: ["coach", shotId],
    queryFn: async () => {
      try {
        return await backend.getCoachSuggestion(shotId);
      } catch (e) {
        if (e instanceof MachineError && e.status === 404) return null;
        throw e;
      }
    },
    enabled: !!shotId,
  });

  const run = useMutation({
    mutationFn: () => backend.coachShot(shotId),
    onSuccess: (data) => {
      qc.setQueryData<CoachSuggestion | null>(["coach", shotId], data);
    },
  });

  const sug: CoachSuggestion | null | undefined = run.data ?? cached.data;
  const isAIDisabled =
    (cached.error instanceof MachineError && cached.error.status === 503) ||
    (run.error instanceof MachineError && run.error.status === 503);

  return (
    <Card
      title="Coach — next pull"
      actions={
        sug ? (
          <button
            onClick={() => run.mutate()}
            disabled={run.isPending}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-xs hover:bg-neutral-800 disabled:opacity-50"
          >
            {run.isPending ? "Thinking…" : "Re-run"}
          </button>
        ) : null
      }
    >
      {isAIDisabled ? (
        <p className="text-sm text-neutral-400">
          AI coach is not configured.{" "}
          <a href="/settings" className="text-emerald-400 underline hover:text-emerald-300">
            Add a provider and API key under Settings
          </a>
          .
        </p>
      ) : cached.isLoading ? (
        <p className="text-sm text-neutral-500">Checking for a previous suggestion…</p>
      ) : !sug ? (
        <div className="space-y-3">
          <p className="text-sm text-neutral-400">
            Ask the coach for ONE focused change to try on your next pull.
            Considers your recent shots on this profile.
          </p>
          <button
            onClick={() => run.mutate()}
            disabled={run.isPending}
            className="rounded-md border border-sky-800 bg-sky-900/40 px-3 py-1.5 text-sm text-sky-200 hover:bg-sky-900/60 disabled:opacity-50"
          >
            {run.isPending ? "Thinking…" : "Get suggestion"}
          </button>
          {run.error && !isAIDisabled && (
            <ErrorBox title="Coach failed" detail={String(run.error)} />
          )}
        </div>
      ) : (
        <SuggestionBody s={sug} profileId={profileId} />
      )}
    </Card>
  );
}

function SuggestionBody({ s, profileId }: { s: CoachSuggestion; profileId?: string }) {
  const hasNumbers =
    s.before != null && s.after != null && Number.isFinite(s.before) && Number.isFinite(s.after);
  const canApply = !!s.var_key && s.after != null && Number.isFinite(s.after);
  return (
    <div className="space-y-4">
      <div className="rounded-lg border border-sky-900/60 bg-sky-950/30 p-4">
        <div className="mb-1 flex items-center gap-2">
          <span className="text-xs uppercase tracking-wide text-sky-300">Try next</span>
          <ConfidencePill level={s.confidence} />
        </div>
        <p className="text-[15px] font-medium text-neutral-100">{s.change}</p>
        {s.rationale && (
          <p className="mt-2 text-sm leading-relaxed text-neutral-300">{s.rationale}</p>
        )}
        {hasNumbers && (
          <div className="mt-3 flex items-center gap-2 text-xs">
            {s.var_key && (
              <code className="rounded bg-neutral-900 px-1.5 py-0.5 font-mono text-neutral-300">
                {s.var_key}
              </code>
            )}
            <span className="font-mono text-neutral-400">
              {fmtNum(s.before!)} → <span className="text-emerald-300">{fmtNum(s.after!)}</span>
            </span>
          </div>
        )}
        {canApply && (
          <div className="mt-3">
            <ApplyProfileChange
              profileId={profileId}
              action={{
                kind: "set_variable",
                variableKey: s.var_key!,
                value: s.after!,
              }}
            />
          </div>
        )}
      </div>
      <footer className="flex flex-wrap items-center justify-between gap-2 border-t border-neutral-900 pt-3 text-[11px] text-neutral-500">
        <span>
          model:{" "}
          <code className="rounded bg-neutral-900 px-1.5 py-0.5 font-mono text-neutral-300">
            {s.model}
          </code>
        </span>
        <span>{new Date(s.created_at).toLocaleString()}</span>
      </footer>
    </div>
  );
}

function ConfidencePill({ level }: { level: CoachSuggestion["confidence"] }) {
  const colors: Record<string, string> = {
    low: "border-neutral-700 bg-neutral-900 text-neutral-400",
    medium: "border-sky-800 bg-sky-900/40 text-sky-200",
    high: "border-emerald-800 bg-emerald-900/40 text-emerald-200",
  };
  return (
    <span
      className={`rounded-full border px-2 py-0.5 text-[10px] uppercase tracking-wide ${
        colors[level] ?? colors.medium
      }`}
    >
      {level} confidence
    </span>
  );
}

function fmtNum(n: number): string {
  if (Number.isInteger(n)) return String(n);
  const abs = Math.abs(n);
  if (abs >= 100) return n.toFixed(0);
  if (abs >= 10) return n.toFixed(1);
  return n.toFixed(2);
}
