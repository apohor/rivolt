// Shot-to-shot comparator panel: pick another shot on the same profile
// and ask the LLM to explain the key differences.
//
// Mirrors ShotAnalysisPanel / ShotCoachPanel visually and behaviourally:
//   - On peer selection we GET any cached comparison for the (this, peer)
//     pair so the user sees prior work immediately.
//   - "Compare" POSTs a fresh run; "Re-run" replaces the cached entry.
//   - The response is parsed into structured sections (Headline,
//     Metric diffs, Likely cause, Next move) and rendered with the
//     same SectionCard + hero chrome as the analysis panel rather than
//     dumping raw markdown.
import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import ReactMarkdown, { type Components } from "react-markdown";
import {
  backend,
  MachineError,
  type ShotComparison,
  type ShotListItem,
} from "../lib/api";
import { Card, ErrorBox } from "./ui";

type Props = {
  shotId: string;
  profileId?: string;
};

export default function ShotComparePanel({ shotId, profileId }: Props) {
  const qc = useQueryClient();
  const [peerId, setPeerId] = useState<string>("");

  const list = useQuery({
    queryKey: ["shots-list", 200],
    queryFn: () => backend.listShots(200),
  });

  const peers = useMemo<ShotListItem[]>(() => {
    const all = list.data ?? [];
    return all.filter(
      (s) => s.id !== shotId && (profileId ? s.profile_id === profileId : true),
    );
  }, [list.data, shotId, profileId]);

  // Cached comparison for the currently-selected pair. We only fire
  // the GET once the user picks a peer; before that there's nothing
  // to look up. 404 is the expected "never compared" state and we
  // swallow it so the empty-state CTA renders.
  const cached = useQuery({
    queryKey: ["compare", shotId, peerId],
    queryFn: async () => {
      try {
        return await backend.getComparison(shotId, peerId);
      } catch (e) {
        if (e instanceof MachineError && e.status === 404) return null;
        throw e;
      }
    },
    enabled: !!peerId,
  });

  const run = useMutation({
    mutationFn: () => backend.compareShots(shotId, peerId),
    onSuccess: (data) => {
      qc.setQueryData<ShotComparison | null>(
        ["compare", shotId, peerId],
        data,
      );
    },
  });

  const comparison: ShotComparison | null | undefined = run.data ?? cached.data;
  const isAIDisabled =
    (cached.error instanceof MachineError && cached.error.status === 503) ||
    (run.error instanceof MachineError && run.error.status === 503);

  return (
    <Card
      title="Compare with another shot"
      actions={
        comparison ? (
          <button
            onClick={() => run.mutate()}
            disabled={!peerId || run.isPending}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-xs hover:bg-neutral-800 disabled:opacity-50"
          >
            {run.isPending ? "Re-running…" : "Re-run"}
          </button>
        ) : null
      }
    >
      <div className="space-y-4">
        <PeerPicker
          peers={peers}
          peerId={peerId}
          setPeerId={(id) => {
            // Clear any staged POST result when the user switches peers
            // so stale text doesn't flash before the new cache query
            // resolves.
            run.reset();
            setPeerId(id);
          }}
          onCompare={() => run.mutate()}
          isPending={run.isPending}
          hasPeers={peers.length > 0}
          loadingPeers={list.isLoading}
        />
        {isAIDisabled ? (
          <p className="text-sm text-neutral-400">
            AI is not configured.{" "}
            <a
              href="/settings"
              className="text-emerald-400 underline hover:text-emerald-300"
            >
              Add a provider and API key under Settings
            </a>
            .
          </p>
        ) : !peerId ? null : cached.isLoading ? (
          <p className="text-sm text-neutral-500">
            Checking for a previous comparison…
          </p>
        ) : run.error &&
          !(run.error instanceof MachineError && run.error.status === 503) ? (
          <ErrorBox title="Compare failed" detail={String(run.error)} />
        ) : comparison ? (
          <ComparisonBody comparison={comparison} />
        ) : (
          <div className="rounded-md border border-dashed border-neutral-800 bg-neutral-950/40 p-4 text-sm text-neutral-400">
            No cached comparison for this pair yet. Click{" "}
            <span className="font-medium text-neutral-200">Compare</span> to run
            one — the result is saved so you only pay for it once.
          </div>
        )}
      </div>
    </Card>
  );
}

// PeerPicker keeps the select + primary CTA visually distinct from the
// result card below, matching the analysis panel's "action bar above,
// body below" rhythm.
function PeerPicker({
  peers,
  peerId,
  setPeerId,
  onCompare,
  isPending,
  hasPeers,
  loadingPeers,
}: {
  peers: ShotListItem[];
  peerId: string;
  setPeerId: (id: string) => void;
  onCompare: () => void;
  isPending: boolean;
  hasPeers: boolean;
  loadingPeers: boolean;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <label className="text-xs uppercase tracking-wide text-neutral-500">
        vs
      </label>
      <select
        value={peerId}
        onChange={(e) => setPeerId(e.target.value)}
        className="flex-1 min-w-[12rem] rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100 disabled:opacity-50"
        disabled={!hasPeers}
      >
        <option value="">
          {loadingPeers
            ? "Loading shots…"
            : hasPeers
              ? "Pick a shot to compare with…"
              : "No siblings on this profile yet"}
        </option>
        {peers.map((s) => (
          <option key={s.id} value={s.id}>
            {shotLabel(s)}
          </option>
        ))}
      </select>
      <button
        onClick={onCompare}
        disabled={!peerId || isPending}
        className="rounded-md border border-violet-800 bg-violet-900/40 px-3 py-1.5 text-sm text-violet-200 hover:bg-violet-900/60 disabled:opacity-50"
      >
        {isPending ? "Comparing…" : "Compare"}
      </button>
    </div>
  );
}

// ComparisonBody renders the LLM's markdown into scannable sections,
// falling back to raw markdown when parsing finds no structure (e.g.
// older cached comparisons or a model that ignored the template).
function ComparisonBody({ comparison }: { comparison: ShotComparison }) {
  const parsed = parseComparison(comparison.markdown);
  return (
    <div className="space-y-4">
      {parsed.headline ? (
        <div className="rounded-lg border border-violet-900/60 bg-violet-950/30 p-4 text-[15px] leading-relaxed text-neutral-100">
          <ReactMarkdown components={inlineMarkdownComponents}>
            {parsed.headline}
          </ReactMarkdown>
        </div>
      ) : null}
      {parsed.ok ? (
        <div className="grid gap-4 md:grid-cols-2">
          {parsed.diffs.length > 0 && (
            <SectionCard title="Metric diffs" accent="sky">
              <ul className="space-y-2">
                {parsed.diffs.map((d, i) => (
                  <li key={i} className="flex items-start gap-2">
                    <span
                      className="mt-1 h-1.5 w-1.5 shrink-0 rounded-full bg-sky-500"
                      aria-hidden
                    />
                    <ReactMarkdown components={inlineMarkdownComponents}>
                      {d}
                    </ReactMarkdown>
                  </li>
                ))}
              </ul>
            </SectionCard>
          )}
          <div className="space-y-4">
            {parsed.cause && (
              <SectionCard title="Likely cause" accent="amber">
                <ReactMarkdown components={markdownComponents}>
                  {parsed.cause}
                </ReactMarkdown>
              </SectionCard>
            )}
            {parsed.nextMove && (
              <SectionCard title="Next move" accent="emerald">
                <ReactMarkdown components={markdownComponents}>
                  {parsed.nextMove}
                </ReactMarkdown>
              </SectionCard>
            )}
          </div>
        </div>
      ) : (
        <div className="rounded-md border border-neutral-800 bg-neutral-950/40 p-4 text-[15px] leading-relaxed text-neutral-200">
          <ReactMarkdown components={markdownComponents}>
            {comparison.markdown}
          </ReactMarkdown>
        </div>
      )}
      <footer className="flex flex-wrap items-center justify-between gap-2 border-t border-neutral-900 pt-3 text-[11px] text-neutral-500">
        <span>
          model:{" "}
          <code className="rounded bg-neutral-900 px-1.5 py-0.5 font-mono text-neutral-300">
            {comparison.model}
          </code>
        </span>
        <span>{new Date(comparison.created_at).toLocaleString()}</span>
      </footer>
    </div>
  );
}

// SectionCard matches the one in ShotAnalysisPanel so the two features
// feel like one product. Kept local to avoid premature cross-component
// sharing until a third caller needs it.
function SectionCard({
  title,
  accent,
  children,
}: {
  title: string;
  accent: "sky" | "emerald" | "amber";
  children: React.ReactNode;
}) {
  const bar =
    accent === "sky"
      ? "bg-sky-600"
      : accent === "amber"
        ? "bg-amber-600"
        : "bg-emerald-600";
  return (
    <section className="overflow-hidden rounded-lg border border-neutral-800 bg-neutral-900/30">
      <header className="flex items-center gap-2 border-b border-neutral-800 px-4 py-2.5">
        <span className={`h-4 w-1 rounded-full ${bar}`} aria-hidden />
        <h3 className="text-xs font-semibold uppercase tracking-wide text-neutral-300">
          {title}
        </h3>
      </header>
      <div className="px-4 py-3 text-[14px] leading-relaxed text-neutral-200">
        {children}
      </div>
    </section>
  );
}

// --- Markdown parsing -----------------------------------------------------
//
// Compare prompt produces this shape:
//
//   ## Headline
//   <one sentence>
//
//   ## Metric diffs
//   - <bullet>
//   - <bullet>
//
//   ## Likely cause
//   <1–3 sentences>
//
//   ## Next move
//   <one sentence>

type ParsedComparison = {
  headline: string;
  diffs: string[];
  cause: string;
  nextMove: string;
  ok: boolean;
};

const HEADING_RE = /^\s*##+\s+(.+?)\s*$/;

function parseComparison(markdown: string): ParsedComparison {
  const lines = markdown.split("\n");
  const sections: { name: string; body: string[] }[] = [
    { name: "__pre__", body: [] },
  ];
  for (const line of lines) {
    const m = line.match(HEADING_RE);
    if (m) {
      sections.push({ name: m[1].toLowerCase(), body: [] });
    } else {
      sections[sections.length - 1].body.push(line);
    }
  }
  const find = (needle: RegExp): string =>
    (
      sections.find((s) => s.name !== "__pre__" && needle.test(s.name))?.body ??
      []
    )
      .join("\n")
      .trim();

  const headline = find(/headline|summary/);
  const diffsBody = find(/diff|metric|numbers/);
  const cause = find(/cause|why|reason/);
  const nextMove = find(/next|action|try|move|suggest/);

  // Parse bullets leniently — accept "- ", "* ", and "1. ".
  const diffs = diffsBody
    .split("\n")
    .map((l) => l.replace(/^\s*(?:[-*]|\d+\.)\s+/, "").trim())
    .filter((l) => l.length > 0);

  const ok = !!(headline || diffs.length > 0 || cause || nextMove);
  return { headline, diffs, cause, nextMove, ok };
}

function shotLabel(s: ShotListItem): string {
  const when = new Date(s.time * 1000).toLocaleString();
  const rating = s.rating ? ` ★${s.rating}` : "";
  return `${s.name || "(unnamed)"} — ${when}${rating}`;
}

// Block-level markdown components for section bodies, inline variants
// for single-line contexts (bullets, headline).
const markdownComponents: Components = {
  p: (p) => <p className="mb-2 last:mb-0" {...p} />,
  strong: (p) => <strong className="font-semibold text-neutral-50" {...p} />,
  em: (p) => <em className="text-neutral-300" {...p} />,
  code: (p) => (
    <code
      className="rounded bg-neutral-800/70 px-1 py-0.5 font-mono text-[12px] text-amber-200"
      {...p}
    />
  ),
  a: (p) => (
    <a className="text-emerald-400 underline hover:text-emerald-300" {...p} />
  ),
  ul: (p) => <ul className="ml-4 list-disc space-y-1" {...p} />,
  ol: (p) => <ol className="ml-4 list-decimal space-y-1" {...p} />,
};

const inlineMarkdownComponents: Components = {
  p: ({ children }) => <>{children}</>,
  strong: (p) => <strong className="font-semibold text-neutral-50" {...p} />,
  em: (p) => <em className="text-neutral-300" {...p} />,
  code: (p) => (
    <code
      className="rounded bg-neutral-800/70 px-1 py-0.5 font-mono text-[12px] text-amber-200"
      {...p}
    />
  ),
};
