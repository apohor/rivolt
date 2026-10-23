import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend, type SiteNotice } from "../lib/api";

// The server's in-app notice (GET /api/auth/me "notice"; today the operator-transfer
// notice the Terms promise 30 days ahead). One source for the web and the iPhone:
// the text and dates come from the server's config, nothing here is hard-coded.

const dismissKey = (id: string) => `rivolt:notice-dismissed:${id}`;

function readDismissed(id: string): boolean {
  try {
    return localStorage.getItem(dismissKey(id)) === "1";
  } catch {
    return false;
  }
}

// activeNotice is the notice to show now: the server's, inside its own window.
export function activeNotice(n: SiteNotice | null | undefined, now = Date.now()): SiteNotice | null {
  if (!n) return null;
  const start = Date.parse(n.starts_at);
  const until = Date.parse(n.until);
  if (Number.isFinite(start) && now < start) return null;
  if (Number.isFinite(until) && now >= until) return null;
  return n;
}

function useSiteNotice(): SiteNotice | null {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  return activeNotice(me.data?.notice);
}

function NoticeText({ n }: { n: SiteNotice }) {
  return (
    <>
      {n.text}
      {n.link_url && n.link_url.startsWith("/") && (
        <>
          {" "}
          <Link to={n.link_url} className="underline underline-offset-2 hover:text-neutral-50">
            {n.link_text || "Details"}
          </Link>
        </>
      )}
    </>
  );
}

// SiteNoticeBanner: one dismissible line on every signed-in page. Dismissal is per
// device (this browser), keyed by the notice's id; the Settings line stays.
export function SiteNoticeBanner() {
  const n = useSiteNotice();
  const [hidden, setHidden] = useState<string | null>(null);
  if (!n || hidden === n.id || readDismissed(n.id)) return null;
  return (
    <div role="region" aria-label="Notice" className="border-b border-sky-900/60 bg-sky-950" data-site-notice>
      <div className="mx-auto flex max-w-6xl items-start gap-3 px-4 py-2 text-xs text-sky-100">
        <p className="flex-1 leading-snug">
          <NoticeText n={n} />
        </p>
        <button
          type="button"
          onClick={() => {
            try {
              localStorage.setItem(dismissKey(n.id), "1");
            } catch {
              /* private mode: hidden for this visit only */
            }
            setHidden(n.id);
          }}
          aria-label="Dismiss notice"
          className="-my-1 min-h-8 min-w-8 rounded-md px-2 text-sky-200 hover:bg-sky-900/60 hover:text-sky-50"
        >
          ✕
        </button>
      </div>
    </div>
  );
}

// SiteNoticeLine: the same notice on Settings → Account, for as long as it runs; not dismissible.
export function SiteNoticeLine() {
  const n = useSiteNotice();
  if (!n) return null;
  return (
    <p className="rounded-md border border-sky-900/60 bg-sky-950/60 px-3 py-2 text-sm text-sky-100" data-site-notice-line>
      <NoticeText n={n} />
    </p>
  );
}
