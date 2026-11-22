import { Link } from "react-router-dom";
import { LegalDoc } from "../components/LegalDoc";
import terms1023 from "../content/terms-2026-10-23.md?url&no-inline";
import terms1117 from "../content/terms-2026-11-17.md?url&no-inline";

// /terms/2026-10-23 and /terms/2026-11-17: the Terms of Service as of those dates, word for word what accounts accepted.
// The texts are src/content/terms-2026-10-23.md and terms-2026-11-17.md and are FROZEN (terms-frozen.test.ts pins their hashes): they
// must not change when the current Terms do. Public like /terms, and kept at stable addresses so the current Terms and the launch notice
// can link to them. Both live in one chunk so the shared text compresses once.
const VERSIONS = {
  "2026-10-23": { src: terms1023, label: "October 23, 2026" },
  "2026-11-17": { src: terms1117, label: "November 17, 2026" },
} as const;

export default function TermsArchivePage({ version = "2026-10-23" }: { version?: keyof typeof VERSIONS }) {
  const { src, label } = VERSIONS[version];
  return (
    <LegalDoc
      src={src}
      other={{ to: "/terms", label: "Current Terms" }}
      top={
        <p data-terms-archive className="rounded-lg border border-sky-800/60 bg-sky-950/40 px-3 py-2 text-sm text-sky-100">
          This is the version of the Terms dated {label}, kept at this address so you can read exactly what you accepted. It does not change. The
          current Terms are at <Link to="/terms" className="underline underline-offset-2">/terms</Link>.
        </p>
      }
    />
  );
}
