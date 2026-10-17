import { LegalDoc } from "../components/LegalDoc";
import policy from "../content/privacy.md?raw";

// /privacy: the short privacy policy, public (works signed out), from src/content/privacy.md.
export default function PrivacyPage() {
  return <LegalDoc source={policy} other={{ to: "/terms", label: "Terms" }} />;
}
