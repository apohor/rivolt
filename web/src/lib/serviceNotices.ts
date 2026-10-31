import type { ServiceNotice } from "./api";

// The notice the Now banner shows: the first one the person has not dismissed.
export function bannerNotice(list: ServiceNotice[] | undefined): ServiceNotice | null {
  return list?.find((n) => !n.dismissed) ?? null;
}

// A link from the server is a path of this site or an https address; anything else (javascript:, data:, a bare string) is not a link.
export function noticeHref(href: string): { kind: "internal" | "external"; href: string } | null {
  if (href.startsWith("/") && !href.startsWith("//")) return { kind: "internal", href };
  try {
    const u = new URL(href);
    return u.protocol === "https:" ? { kind: "external", href: u.toString() } : null;
  } catch {
    return null;
  }
}
