import { LegalDoc } from "../components/LegalDoc";
import guide from "../content/r2.md?raw";

// /r2: the first week with an R2 in Rivolt, public (works signed out), rendered from src/content/r2.md like /safe-to-connect. Its
// "recorded" and "not yet" lists must match the Truck card's R2 checked list (R2CheckedContent) item for item; r2.test.ts checks it.
export default function R2GuidePage() {
  return <LegalDoc source={guide} other={{ to: "/safe-to-connect", label: "Is it safe to connect?" }} />;
}
