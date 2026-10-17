import Markdown from "react-markdown";
import { Link } from "react-router-dom";
import Logo from "../components/Logo";
import policy from "../content/privacy.md?raw";
import { useSystemTheme } from "../lib/theme";

// /privacy: the short privacy policy, public (works signed out), rendered from a single markdown
// source at src/content/privacy.md. Styled with the theme tokens, so it follows light and dark.
export default function PrivacyPage() {
  // Public page: follow the device theme (light, dark or system) like the demo does.
  useSystemTheme(true);
  return (
    <div className="min-h-full bg-neutral-950 text-neutral-200">
      <header className="border-b border-neutral-900">
        <div className="mx-auto flex max-w-3xl items-center justify-between gap-3 px-4 py-3">
          <Link to="/" aria-label="Rivolt home" className="inline-flex items-center gap-2 text-sm font-semibold text-neutral-100">
            <Logo />
          </Link>
          <Link to="/" className="text-sm text-neutral-400 hover:text-neutral-100">
            Back to Rivolt
          </Link>
        </div>
      </header>
      <main className="mx-auto max-w-3xl px-4 py-8 sm:py-12" data-privacy>
        <article className="space-y-4 text-[15px] leading-relaxed">
          <Markdown
            components={{
              h1: (p) => <h1 className="text-3xl font-semibold tracking-tight text-neutral-50" {...p} />,
              h2: (p) => <h2 className="pt-4 text-xl font-semibold text-neutral-50" {...p} />,
              p: (p) => <p className="text-neutral-300" {...p} />,
              ul: (p) => <ul className="list-disc space-y-2 pl-5 text-neutral-300 marker:text-neutral-600" {...p} />,
              li: (p) => <li {...p} />,
              strong: (p) => <strong className="font-semibold text-neutral-100" {...p} />,
              a: ({ href, ...p }) => (
                <a href={href} className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200" {...(href?.startsWith("http") ? { target: "_blank", rel: "noopener noreferrer" } : {})} {...p} />
              ),
            }}
          >
            {policy}
          </Markdown>
        </article>
      </main>
      <footer className="border-t border-neutral-900 text-xs text-neutral-500">
        <div className="mx-auto max-w-3xl px-4 py-4">Rivolt is independent and not affiliated with Rivian.</div>
      </footer>
    </div>
  );
}
