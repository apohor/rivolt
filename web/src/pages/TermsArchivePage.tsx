import { Link } from "react-router-dom";
import { LegalDoc } from "../components/LegalDoc";
import terms1023 from "../content/terms-2026-10-23.md?url&no-inline";
import terms1117 from "../content/terms-2026-11-17.md?url&no-inline";
import terms1121 from "../content/terms-2026-11-21.md?url&no-inline";
import terms1122 from "../content/terms-2026-11-22.md?url&no-inline";
import terms1125 from "../content/terms-2026-11-25.md?url&no-inline";
import { TERMS_VERSIONS, type TermsVersion } from "../lib/termsVersions";

// /terms/2026-10-23, /terms/2026-11-17 and /terms/2026-11-21, /terms/2026-11-22, /terms/2026-11-25: the Terms of Service as of those dates, word for word what accounts accepted.
// The texts are src/content/terms-2026-10-23.md, terms-2026-11-17.md , terms-2026-11-21.md and terms-2026-11-22.md and terms-2026-11-25.md and are FROZEN (terms-frozen.test.ts pins their hashes): they
// must not change when the current Terms do. Public like /terms, and kept at stable addresses so the current Terms and the launch notice
// can link to them. Both live in one chunk so the shared text compresses once.
// The versions and their labels are in lib/termsVersions.ts (the list /terms shows); a version listed there without a text here does not compile.
const SOURCES: Record<TermsVersion, string> = { "2026-10-23": terms1023, "2026-11-17": terms1117, "2026-11-21": terms1121, "2026-11-22": terms1122, "2026-11-25": terms1125 };

export default function TermsArchivePage({ version = "2026-10-23" }: { version?: TermsVersion }) {
  const src = SOURCES[version];
  const label = TERMS_VERSIONS.find((v) => v.key === version)!.label;
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
