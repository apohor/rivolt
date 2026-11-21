import terms from "../content/terms-2026-11-17.md?raw";
import { TermsArchive } from "./TermsArchivePage";

// /terms/2026-11-17: the Terms as published on launch day, November 17, 2026. FROZEN like the October 23 version
// (terms-frozen.test.ts pins its hash); the current Terms moved on when the word "beta" came out of section 5.
export default function TermsArchive20261117Page() {
  return <TermsArchive source={terms} dateLabel="November 17, 2026" />;
}
