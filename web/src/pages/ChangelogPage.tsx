import { useEffect } from "react";
import Markdown from "react-markdown";
import { Link, useLocation } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import Logo from "../components/Logo";
import { backend, type ChangelogRelease } from "../lib/api";
import { useSystemTheme } from "../lib/theme";

// /changelog: what's new in each release, public (works signed out). The notes are the ones written
// for each GitHub Release (GET /api/changelog), newest first; each release is a section with
// id="vX.Y.Z", which is where the Atom feed (/changelog.atom) links. The notes are Markdown rendered
// without raw HTML: any HTML in them (even a comment) is dropped, neither rendered nor shown as text.

const dateFmt = new Intl.DateTimeFormat("en-US", { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" });

function releaseDate(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : dateFmt.format(d);
}

const linkCls = "text-emerald-300 underline underline-offset-2 hover:text-emerald-200";

function Notes({ source }: { source: string }) {
  return (
    <Markdown
      skipHtml
      components={{
        // The release title is the section's h2, so the notes' own headings sit one level below it.
        h1: (p) => <h3 className="pt-2 text-lg font-semibold text-neutral-50" {...p} />,
        h2: (p) => <h3 className="pt-2 text-lg font-semibold text-neutral-50" {...p} />,
        h3: (p) => <h4 className="pt-1 font-semibold text-neutral-100" {...p} />,
        p: (p) => <p className="text-neutral-300" {...p} />,
        ul: (p) => <ul className="list-disc space-y-2 pl-5 text-neutral-300 marker:text-neutral-600" {...p} />,
        ol: (p) => <ol className="list-decimal space-y-2 pl-5 text-neutral-300" {...p} />,
        strong: (p) => <strong className="font-semibold text-neutral-100" {...p} />,
        code: (p) => <code className="rounded-sm bg-neutral-900 px-1 py-0.5 text-sm text-neutral-200" {...p} />,
        img: () => null,
        a: ({ href, children }) =>
          // In-app paths only; "//host" is another site and opens like any external link.
          href?.startsWith("/") && !href.startsWith("//") ? (
            <Link to={href} className={linkCls}>{children}</Link>
          ) : (
            <a href={href} className={linkCls} target="_blank" rel="noopener noreferrer">{children}</a>
          ),
      }}
    >
      {source}
    </Markdown>
  );
}

function Release({ r }: { r: ChangelogRelease }) {
  const date = releaseDate(r.published_at);
  return (
    <section id={r.tag} className="scroll-mt-4 space-y-3 border-t border-neutral-900 pt-6" aria-labelledby={`${r.tag}-title`}>
      <h2 id={`${r.tag}-title`} className="flex flex-wrap items-baseline gap-x-3 text-xl font-semibold text-neutral-50">
        <a href={`#${r.tag}`} className="hover:underline">{r.tag}</a>
        {date && <span className="text-sm font-normal text-neutral-400">{date}</span>}
      </h2>
      {/* Older notes carry long unbroken URLs ("Full Changelog: https://…/compare/…"); let them wrap anywhere so
          they never widen the page on a phone. */}
      <div className="min-w-0 space-y-3 text-base leading-relaxed [overflow-wrap:anywhere]">
        <Notes source={r.body_md} />
      </div>
      {r.url && (
        <p className="text-sm">
          <a href={r.url} className={linkCls} target="_blank" rel="noopener noreferrer">
            Release on GitHub
          </a>
        </p>
      )}
    </section>
  );
}

export default function ChangelogPage() {
  useSystemTheme(true);
  const { hash } = useLocation();
  const q = useQuery({ queryKey: ["changelog"], queryFn: backend.changelog, staleTime: 5 * 60 * 1000, retry: 1 });
  const releases = q.data?.releases ?? [];
  // Land on #vX.Y.Z once the releases are there (the feed links here).
  useEffect(() => {
    if (!hash || releases.length === 0) return;
    document.getElementById(decodeURIComponent(hash.slice(1)))?.scrollIntoView({ block: "start" });
  }, [hash, releases.length]);

  return (
    <div className="min-h-full bg-neutral-950 text-neutral-200">
      <header className="border-b border-neutral-900">
        <div className="mx-auto flex max-w-3xl items-center justify-between gap-3 px-4 py-3">
          <Link to="/" aria-label="Rivolt home" className="inline-flex items-center gap-2 text-sm font-semibold text-neutral-100">
            <Logo />
          </Link>
          <nav aria-label="Changelog" className="flex items-center gap-4 text-sm">
            <a href="/changelog.atom" className="text-neutral-400 hover:text-neutral-100">Feed</a>
            <Link to="/" className="text-neutral-400 hover:text-neutral-100">Back to Rivolt</Link>
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-3xl space-y-6 px-4 py-8 sm:py-12" data-changelog>
        <div className="space-y-2">
          <h1 className="text-3xl font-semibold tracking-tight text-neutral-50">What's new in Rivolt</h1>
          <p className="text-neutral-400">
            What changed in each release, newest first. Follow along with the{" "}
            <a href="/changelog.atom" className={linkCls}>Atom feed</a>.
          </p>
        </div>
        {q.isPending && <p className="text-neutral-400">Loading release notes…</p>}
        {q.isError && <p className="text-neutral-400">Release notes are unavailable right now. Try again in a few minutes.</p>}
        {q.isSuccess && releases.length === 0 && <p className="text-neutral-400">No release notes yet.</p>}
        {releases.map((r) => <Release key={r.tag} r={r} />)}
      </main>
      <footer className="border-t border-neutral-900 text-xs text-neutral-500">
        <div className="mx-auto max-w-3xl px-4 py-4">Rivolt is independent and not affiliated with Rivian.</div>
      </footer>
    </div>
  );
}
