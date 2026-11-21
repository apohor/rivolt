import { LegalDoc } from "../components/LegalDoc";
import policy from "../content/privacy.md?url&no-inline";

// /privacy: the short privacy policy, public (works signed out), from src/content/privacy.md.
export default function PrivacyPage() {
  return <LegalDoc src={policy} other={{ to: "/terms", label: "Terms" }} />;
}
