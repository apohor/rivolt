import { Link } from "react-router-dom";
import { LegalDoc } from "../components/LegalDoc";
import terms from "../content/terms.md?url&no-inline";
import { TERMS_VERSIONS } from "../lib/termsVersions";

// /terms: the Terms of Service, public (works signed out), from src/content/terms.md. Sections are
// anchored by number (/terms#2). Under the text, a quiet line links every archived version (lib/termsVersions.ts), outside terms.md so
// the Terms text, its date and the frozen copies do not move.
export default function TermsPage() {
  return (
    <LegalDoc
      src={terms}
      other={{ to: "/privacy", label: "Privacy" }}
      bottom={
        <p data-terms-earlier className="pt-4 text-sm text-neutral-500">
          Earlier versions:{" "}
          {TERMS_VERSIONS.map((v, i) => (
            <span key={v.key}>
              {i > 0 && ", "}
              <Link to={`/terms/${v.key}`} className="underline underline-offset-2 hover:text-neutral-300">{v.label}</Link>
            </span>
          ))}
          .
        </p>
      }
    />
  );
}
