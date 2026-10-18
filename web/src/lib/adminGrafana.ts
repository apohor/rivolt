import { grafanaBaseURL } from "./config";

// grafanaUserExploreURL builds Grafana Explore deep links scoped
// to one user_id over a time window. Returns "" when no Grafana
// origin is wired so callers fall back to plain text.
//
// We emit two flavours of link:
//   - logs:   {namespace=~"rivolt.*"} |~ "<uid>" against Loki —
//             matches both server slog lines and any free-form
//             mention of the uid.
//   - traces: { .user.id = "<uid>" } against Tempo TraceQL —
//             relies on the user.id span attribute stamped in
//             auth.WithUser (v0.18.69+). Older spans won't match.
export function grafanaUserExploreURL(
  userID: string,
  fromISO: string,
  toISO: string,
  kind: "logs" | "traces" = "logs",
): string {
  const base = grafanaBaseURL();
  if (!base) return "";
  const from = new Date(fromISO).getTime();
  const to = new Date(toISO).getTime();
  if (!Number.isFinite(from) || !Number.isFinite(to)) return "";
  // Grafana 11+ replaced the `?left=` URL param with a new
  // `?panes=<encoded>&schemaVersion=1` shape keyed by a
  // randomly-generated pane id. The legacy `left` param is
  // still parsed but the datasource field gets dropped on the
  // floor — Grafana then defaults to the first provisioned
  // datasource of any type (Tempo, in our case), which is the
  // "logs link opens Tempo" symptom. Build the new format.
  const ds =
    kind === "logs"
      ? { type: "loki", uid: "loki" }
      : { type: "tempo", uid: "tempo" };
  const query =
    kind === "logs"
      ? {
          refId: "A",
          datasource: ds,
          expr: `{namespace=~"rivolt.*"} |~ "${userID}"`,
        }
      : {
          refId: "A",
          datasource: ds,
          queryType: "traceql",
          query: `{ .user.id = "${userID}" }`,
        };
  const pane = {
    datasource: ds.uid,
    queries: [query],
    range: { from: String(from), to: String(to) },
  };
  // Pane id is an arbitrary short key Grafana uses to identify
  // each split. Keep it deterministic per-kind for predictable
  // URLs but it could be any string.
  const paneID = kind === "logs" ? "lg" : "tr";
  const panes = { [paneID]: pane };
  return `${base}/explore?schemaVersion=1&orgId=1&panes=${encodeURIComponent(JSON.stringify(panes))}`;
}
