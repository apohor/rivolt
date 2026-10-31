import { Link } from "react-router-dom";
import { LegalDoc } from "../components/LegalDoc";
import terms from "../content/terms-2026-10-23.md?raw";

// /terms/2026-10-23: the Terms of Service as of October 23, 2026, word for word what accounts created before the new Terms accepted.
// The text is src/content/terms-2026-10-23.md and is FROZEN (terms-frozen.test.ts pins its hash): it must not change when the current
// Terms do. Public like /terms, and kept at this stable address so the current Terms and the launch notice can link to it.
export default function TermsArchivePage() {
  return (
    <LegalDoc
      source={terms}
      other={{ to: "/terms", label: "Current Terms" }}
      top={
        <p data-terms-archive className="rounded-lg border border-sky-800/60 bg-sky-950/40 px-3 py-2 text-sm text-sky-100">
          This is the version of the Terms dated October 23, 2026, kept at this address so you can read exactly what you accepted. It does not change. The
          current Terms are at <Link to="/terms" className="underline underline-offset-2">/terms</Link>.
        </p>
      }
    />
  );
}
