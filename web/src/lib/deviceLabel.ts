import type { AuthSession } from "./api";

// The name shown for a signed-in device: the label the server stored when there is one, else a plain one made from the browser's
// User-Agent (browser and system, nothing finer), else the kind. Never an address.
export function deviceName(s: Pick<AuthSession, "kind" | "device_label" | "user_agent">): string {
  const label = (s.device_label ?? "").trim();
  if (label) return label;
  if (s.kind === "app") return "iPhone app";
  const ua = s.user_agent ?? "";
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /OPR\//.test(ua)
      ? "Opera"
      : /Firefox\//.test(ua)
        ? "Firefox"
        : /Chrome\//.test(ua)
          ? "Chrome"
          : /Safari\//.test(ua)
            ? "Safari"
            : "";
  const system = /iPhone|iPad/.test(ua)
    ? "iOS"
    : /Android/.test(ua)
      ? "Android"
      : /Mac OS X|Macintosh/.test(ua)
        ? "macOS"
        : /Windows/.test(ua)
          ? "Windows"
          : /Linux|X11/.test(ua)
            ? "Linux"
            : "";
  if (browser && system) return `${browser} on ${system}`;
  return browser || system || "Browser";
}

// "Signed in Oct 24, 9:40 AM · active 5 min ago" is built by the component; this is the "active …" part, as a coarse age.
export function activeAgo(lastSeenIso: string, now: Date = new Date()): string {
  const t = new Date(lastSeenIso).getTime();
  if (!Number.isFinite(t)) return "";
  const s = Math.max(0, Math.round((now.getTime() - t) / 1000));
  if (s < 90) return "just now";
  const m = Math.round(s / 60);
  if (m < 90) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 36) return `${h} h ago`;
  return `${Math.round(h / 24)} days ago`;
}
