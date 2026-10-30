// Which Settings card a link points at: the URL fragment (#danger) or, for links that must survive a sign-in redirect, a ?section=
// query. A fragment never reaches the server, so the bounce through the sign-in page drops it; a query is kept. The iPhone app's
// "Open account deletion" uses ?tab=data&section=delete.
const SECTION_IDS: Record<string, string> = { delete: "danger" };

export function settingsAnchor(search: string, hash: string): string {
  if (hash.length > 1) return hash.slice(1);
  const section = new URLSearchParams(search).get("section") ?? "";
  return Object.prototype.hasOwnProperty.call(SECTION_IDS, section) ? SECTION_IDS[section] : "";
}
