import { useEffect } from "react";
import Markdown from "react-markdown";
import { Link, useLocation } from "react-router-dom";
import Logo from "./Logo";
import { useSystemTheme } from "../lib/theme";

// A public legal page (Privacy, Terms): rendered from one markdown source, styled with the theme
// tokens, public (no layout, no API). Headings that start with a number ("## 2. ...") get id="2",
// so /terms#2 lands on the section.
const textOf = (children: React.ReactNode): string =>
  Array.isArray(children) ? children.map(textOf).join("") : typeof children === "string" ? children : "";

export function LegalDoc({ source, other }: { source: string; other: { to: string; label: string } }) {
  // Follow the device theme (light, dark or system) like the demo does.
  useSystemTheme(true);
  const { hash } = useLocation();
  useEffect(() => {
    if (!hash) return;
    document.getElementById(decodeURIComponent(hash.slice(1)))?.scrollIntoView({ block: "start" });
  }, [hash]);
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
        <article className="space-y-4 text-[15px] leading-relaxed">
          <Markdown
            components={{
              h1: (p) => <h1 className="text-3xl font-semibold tracking-tight text-neutral-50" {...p} />,
              h2: ({ children, ...p }) => {
                const m = textOf(children).match(/^(\d+)\./);
                return (
                  <h2 id={m ? m[1] : undefined} className="scroll-mt-4 pt-4 text-xl font-semibold text-neutral-50" {...p}>
                    {children}
                  </h2>
                );
              },
              p: (p) => <p className="text-neutral-300" {...p} />,
              ul: (p) => <ul className="list-disc space-y-2 pl-5 text-neutral-300 marker:text-neutral-600" {...p} />,
              li: (p) => <li {...p} />,
              strong: (p) => <strong className="font-semibold text-neutral-100" {...p} />,
              code: (p) => <code className="rounded-sm bg-neutral-900 px-1 py-0.5 text-[13px] text-neutral-200" {...p} />,
              a: ({ href, children, ...p }) =>
                href?.startsWith("/") ? (
                  <Link to={href} className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200">{children}</Link>
                ) : (
                  <a href={href} className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200" target="_blank" rel="noopener noreferrer" {...p}>
                    {children}
                  </a>
                ),
            }}
          >
            {source}
          </Markdown>
        </article>
      </main>
      <footer className="border-t border-neutral-900 text-xs text-neutral-500">
        <div className="mx-auto max-w-3xl px-4 py-4">Rivolt is independent and not affiliated with Rivian.</div>
      </footer>
    </div>
  );
}
