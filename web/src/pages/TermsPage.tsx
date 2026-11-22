import { LegalDoc } from "../components/LegalDoc";
import terms from "../content/terms.md?url&no-inline";

// /terms: the Terms of Service, public (works signed out), from src/content/terms.md. Sections are
// anchored by number (/terms#2).
export default function TermsPage() {
  return <LegalDoc src={terms} other={{ to: "/privacy", label: "Privacy" }} />;
}
