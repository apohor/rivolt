import { LegalDoc } from "../components/LegalDoc";
import guide from "../content/r2.md?raw";
import { R2ArrivalControl } from "../components/R2Arrival";
import { useSignedIn } from "../lib/useSignedIn";

// /r2: the first week with an R2 in Rivolt, public (works signed out), rendered from src/content/r2.md like /safe-to-connect. Its
// "recorded" and "not yet" lists must match the Truck card's R2 checked list (R2CheckedContent) item for item; r2.test.ts checks it.
export default function R2GuidePage() {
  // The reminder needs an account: only a signed-in visitor sees it, between the checklist and Day 1.
  const signedIn = useSignedIn();
  return <LegalDoc source={guide} splitAt="## Day 1: connect" between={signedIn ? <R2ArrivalControl where="guide" /> : undefined} other={{ to: "/safe-to-connect", label: "Is it safe to connect?" }} />;
}
