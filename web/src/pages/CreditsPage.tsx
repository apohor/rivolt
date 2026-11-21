import { LegalDoc } from "../components/LegalDoc";
import credits from "../content/credits.md?url&no-inline";

// /credits: data credits and licences (Open-Meteo, NOAA, ECCC, OpenStreetMap), public (works signed out), from src/content/credits.md.
export default function CreditsPage() {
  return <LegalDoc src={credits} other={{ to: "/privacy", label: "Privacy" }} />;
}
