import { LegalDoc } from "../components/LegalDoc";
import support from "../content/support.md?url&no-inline";

// /support: where to get help, public (works signed out), rendered from src/content/support.md like /privacy and /terms.
// It is the App Store's Support URL, so the email address on it must keep working.
export default function SupportPage() {
  return <LegalDoc src={support} other={{ to: "/privacy", label: "Privacy" }} />;
}
