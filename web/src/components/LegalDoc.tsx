import { useEffect } from "react";
import Markdown, { type Components } from "../lib/miniMarkdown";
import { Link, useLocation } from "react-router-dom";
import Logo from "./Logo";
import { useSystemTheme } from "../lib/theme";
import { headingId } from "../lib/headingId";
import { useLegalText } from "../lib/useLegalText";

// A public legal page (Privacy, Terms): rendered from one markdown source, styled with the theme
// tokens, public (no layout, no API). A "##" heading that starts with a number ("## 2. ...") gets id="2", so /terms#2 lands on the
// section; any other gets a slug of its text ("## Free and Pro" -> id="free-and-pro", /support#free-and-pro). See lib/headingId.ts.
const textOf = (children: React.ReactNode): string =>
  Array.isArray(children) ? children.map(textOf).join("") : typeof children === "string" ? children : "";

const mdComponents: Components = {
      h1: (p) => <h1 className="text-3xl font-semibold tracking-tight text-neutral-50" {...p} />,
      h2: ({ children, ...p }) => {
        return (
          <h2 id={headingId(textOf(children))} className="scroll-mt-4 pt-4 text-xl font-semibold text-neutral-50" {...p}>
            {children}
          </h2>
        );
      },
      p: (p) => <p className="text-neutral-300" {...p} />,
      ul: (p) => <ul className="list-disc space-y-2 pl-5 text-neutral-300 marker:text-neutral-600" {...p} />,
      // An ordered list shows its numbers (the reset stylesheet strips them): the R2 guide's five-minute checklist reads as steps.
      ol: (p) => <ol className="list-decimal space-y-2 pl-6 text-neutral-300 marker:font-semibold marker:text-neutral-500" {...p} />,
      li: (p) => <li {...p} />,
      strong: (p) => <strong className="font-semibold text-neutral-100" {...p} />,
      code: (p) => <code className="rounded-sm bg-neutral-900 px-1 py-0.5 text-sm text-neutral-200" {...p} />,
      a: ({ href, children, ...p }) =>
        href?.startsWith("/") ? (
          <Link to={href} className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200">{children}</Link>
        ) : (
          <a href={href} className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200" target="_blank" rel="noopener noreferrer" {...p}>
            {children}
          </a>
        ),
};

// `between` (with `splitAt`, the heading it goes in front of) puts a small interactive block inside the document, after the section
// before that heading; the markdown on either side is rendered the same way.
// The text is `source` (a string, for tests and callers that hold it) or `src` (the URL of a src/content/*.md asset, fetched on open: the page
// shows its header and a loading line meanwhile, and a retry button if the fetch fails).
export function LegalDoc({ source: given, src, other, between, splitAt, top, bottom }: { source?: string; src?: string; other: { to: string; label: string }; between?: React.ReactNode; splitAt?: string; top?: React.ReactNode; bottom?: React.ReactNode }) {
  const fetched = useLegalText(given === undefined ? src : undefined);
  const source = given ?? fetched.text;
  // Follow the device theme (light, dark or system) like the demo does.
  useSystemTheme(true);
  const { hash } = useLocation();
  const at = source !== null && between && splitAt ? source.indexOf(splitAt) : -1;
  const parts: [string, string?] = source === null ? [""] : at > 0 ? [source.slice(0, at), source.slice(at)] : [source];
  useEffect(() => {
    if (!hash) return;
    document.getElementById(decodeURIComponent(hash.slice(1)))?.scrollIntoView({ block: "start" });
  }, [hash, source]);
  return (
    <div className="min-h-full bg-neutral-950 text-neutral-200">
      <header className="border-b border-neutral-900">
        <div className="mx-auto flex max-w-3xl items-center justify-between gap-3 px-4 py-3">
          <Link to="/" aria-label="Rivolt home" className="inline-flex items-center gap-2 text-sm font-semibold text-neutral-100">
            <Logo />
          </Link>
          <nav aria-label="Legal" className="flex items-center gap-4 text-sm">
            <Link to={other.to} className="text-neutral-400 hover:text-neutral-100">{other.label}</Link>
            <Link to="/" className="text-neutral-400 hover:text-neutral-100">Back to Rivolt</Link>
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-3xl px-4 py-8 sm:py-12" data-legal>
        <article className="space-y-4 text-base leading-relaxed">
          {top}
          {source === null && !fetched.failed && <p role="status" className="text-neutral-400">Loading…</p>}
          {source === null && fetched.failed && (
            <p role="alert" className="text-neutral-300">
              This page could not be loaded.{" "}
              <button type="button" onClick={fetched.retry} className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200">Try again</button>
            </p>
          )}
          {source !== null && <Markdown components={mdComponents}>{parts[0]}</Markdown>}
          {parts[1] !== undefined && between}
          {parts[1] !== undefined && <Markdown components={mdComponents}>{parts[1]}</Markdown>}
          {source !== null && bottom}
        </article>
      </main>
      <footer className="border-t border-neutral-900 text-xs text-neutral-500">
        <div className="mx-auto max-w-3xl px-4 py-4">Rivolt is independent and not affiliated with Rivian.</div>
      </footer>
    </div>
  );
}
