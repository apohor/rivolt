import { LegalDoc } from "../components/LegalDoc";
import guide from "../content/driver-account.md?url&no-inline";

// /driver-account: how to set up the Authorized Driver account the Rivian sign-in screen offers (design key signup_v2), public like
// /safe-to-connect and rendered from src/content/driver-account.md. Same steps as docs/SIGNUP.md "Recommended: connect a dedicated
// Authorized Driver account".
export default function DriverAccountPage() {
  return <LegalDoc src={guide} other={{ to: "/safe-to-connect", label: "Is it safe to connect?" }} />;
}
