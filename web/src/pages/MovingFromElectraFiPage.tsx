import { LegalDoc } from "../components/LegalDoc";
import guide from "../content/moving-from-electrafi.md?raw";

// /moving-from-electrafi: how to bring ElectraFi history into Rivolt. Public (works signed out),
// rendered from src/content/moving-from-electrafi.md like /privacy and /terms.
export default function MovingFromElectraFiPage() {
  return <LegalDoc source={guide} other={{ to: "/privacy", label: "Privacy" }} />;
}
