// The archived versions of the Terms, newest first: the one list behind the "Earlier versions" line on /terms and the archive pages
// (TermsArchivePage holds each version's text and does not compile without one for every key here). A new archive starts here.
export const TERMS_VERSIONS = [
  { key: "2026-11-25", label: "November 25, 2026" },
  { key: "2026-11-22", label: "November 22, 2026" },
  { key: "2026-11-21", label: "November 21, 2026" },
  { key: "2026-11-17", label: "November 17, 2026" },
  { key: "2026-10-23", label: "October 23, 2026" },
] as const;

export type TermsVersion = (typeof TERMS_VERSIONS)[number]["key"];
