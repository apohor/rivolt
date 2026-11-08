import { LegalDoc } from "../components/LegalDoc";
import credits from "../content/credits.md?raw";

// /credits: data credits and licences (Open-Meteo, NOAA, ECCC, OpenStreetMap), public (works signed out), from src/content/credits.md.
export default function CreditsPage() {
  return <LegalDoc source={credits} other={{ to: "/privacy", label: "Privacy" }} />;
}
