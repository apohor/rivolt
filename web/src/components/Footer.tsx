import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { inDemo } from "../lib/demo";

export function Footer() {
  const health = useQuery({
    queryKey: ["health"],
    queryFn: () => backend.health(),
    staleTime: 60_000,
  });
  return (
    <footer className="border-t border-neutral-900 bg-neutral-950/80 text-xs text-neutral-500">
      <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-center gap-x-3 gap-y-1 px-4 py-3 text-center">
        <a
          href="https://github.com/apohor/rivolt"
          target="_blank"
          rel="noopener noreferrer"
          className="hover:text-neutral-200"
        >
          GitHub
        </a>
        <span className="text-neutral-700">·</span>
        <span>MIT licensed</span>
        <span className="text-neutral-700">·</span>
        <a
          href="https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md"
          target="_blank"
          rel="noopener noreferrer"
          className="hover:text-neutral-200"
        >
          Docs
        </a>
        <span className="text-neutral-700">·</span>
        <Link to="/privacy" className="hover:text-neutral-200">
          Privacy
        </Link>
        <span className="text-neutral-700">·</span>
        <Link to="/terms" className="hover:text-neutral-200">
          Terms
        </Link>
        {!inDemo() && (
          <>
            <span className="text-neutral-700">·</span>
            <Link to="/lease-calculator" className="hover:text-neutral-200">
              Lease calculator
            </Link>
            <span className="text-neutral-700">·</span>
            <Link to="/changelog" className="hover:text-neutral-200">
              What's new
            </Link>
          </>
        )}
        <span className="text-neutral-700">·</span>
        <a
          href="https://discord.gg/FGty2xmdeV"
          target="_blank"
          rel="noopener noreferrer"
          className="hover:text-neutral-200"
        >
          Discord
        </a>
        {health.data?.version && (() => {
          // Three CI-stamped shapes:
          //   vX.Y.Z-dev.N+sha-<hex> → preview / main builds (link to commit)
          //   sha-<hex>         → legacy preview stamp (link to commit)
          //   X.Y.Z             → tagged prod build (link to release)
          const v = health.data.version;
          const shaMatch = v.match(/sha-([0-9a-f]{7,})$/);
          const href = shaMatch
            ? `https://github.com/apohor/rivolt/commit/${shaMatch[1]}`
            : `https://github.com/apohor/rivolt/releases/tag/v${v}`;
          const label = v.startsWith("v") ? v : `v${v}`;
          const title = shaMatch ? "Open commit on GitHub" : "What changed in this release";
          return (
            <>
              <span className="text-neutral-700">·</span>
              <a
                href={href}
                target="_blank"
                rel="noopener noreferrer"
                className="font-mono text-neutral-500 hover:text-neutral-300"
                title={title}
              >
                {label}
              </a>
            </>
          );
        })()}
      </div>
    </footer>
  );
}
