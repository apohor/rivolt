// The one HTML-entity escape for the few places that put text into HTML strings (map tooltips and popups): saved-trip
// and recap text, OSM names and anything else a user's account or a restore can carry. All five characters, so it is
// safe in element content and in quoted attributes. Everything else in the app renders text through React, which
// escapes it; there is no dangerouslySetInnerHTML (the lint config bans it) and no markdown renderer on that text.
export function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) => (c === "&" ? "&amp;" : c === "<" ? "&lt;" : c === ">" ? "&gt;" : c === '"' ? "&quot;" : "&#39;"));
}
