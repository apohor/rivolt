import { LegalDoc } from "../components/LegalDoc";
import guide from "../content/safe-to-connect.md?raw";

// /safe-to-connect: what connecting a Rivian account means, in plain terms. Public (works signed
// out), rendered from src/content/safe-to-connect.md like /privacy and /terms. Keep it in step
// with the connect step's "Connecting, in plain terms" panel and docs/SIGNUP.md section 2.
export default function SafeToConnectPage() {
  return <LegalDoc source={guide} other={{ to: "/privacy", label: "Privacy" }} />;
}
