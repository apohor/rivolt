import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend, type ServiceNotice } from "../lib/api";
import { bannerNotice, noticeHref } from "../lib/serviceNotices";
import { Card } from "./ui";

function useNotices() {
  // A failure (an older server, no notices table) is "no notices", never an error on the page.
  return useQuery({ queryKey: ["notices"], queryFn: () => backend.notices().catch(() => [] as ServiceNotice[]), staleTime: 5 * 60_000 });
}

function Links({ links }: { links: ServiceNotice["links"] }) {
  const items = (links ?? []).map((l) => ({ label: l.label, to: noticeHref(l.href) })).filter((l) => l.to);
  if (items.length === 0) return null;
  return (
    <span className="flex flex-wrap gap-3">
      {items.map((l) =>
        l.to!.kind === "internal" ? (
          <Link key={l.label + l.to!.href} to={l.to!.href} className="underline underline-offset-2">{l.label}</Link>
        ) : (
          <a key={l.label + l.to!.href} href={l.to!.href} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2">{l.label}</a>
        ),
      )}
    </span>
  );
}

// The banner on Now: the first service notice not yet dismissed (title, body, links), with Dismiss. Nothing when there is none.
export function ServiceNoticeBanner() {
  const q = useNotices();
  const [gone, setGone] = useState<string[]>([]);
  const n = bannerNotice(q.data?.filter((x) => !gone.includes(x.kind)));
  if (!n) return null;
  return (
    <div role="note" data-service-notice={n.kind} className="space-y-1 rounded-lg border border-sky-800/60 bg-sky-950/40 px-3 py-2 text-sm text-sky-100">
      <p className="font-medium">{n.title}</p>
      <p className="text-xs text-sky-200">{n.body}</p>
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <Links links={n.links} />
        <button
          type="button"
          className="text-sky-300 hover:text-sky-100"
          onClick={() => {
            setGone((g) => [...g, n.kind]);
            // Hidden now; if the write fails the banner is back on the next visit, which is fine.
            void backend.dismissNotice(n.kind).catch(() => undefined);
          }}
        >
          Dismiss
        </button>
      </div>
    </div>
  );
}

// Settings → Account: every notice still within its window, dismissed or not, so the words stay readable after the banner is gone.
export function ServiceNoticesCard() {
  const q = useNotices();
  const list = q.data ?? [];
  if (list.length === 0) return null;
  return (
    <Card title="Notices" id="notices">
      <div className="space-y-4">
        {list.map((n) => (
          <div key={n.kind} data-service-notice-row={n.kind} className="space-y-1 text-sm">
            <p className="font-medium text-neutral-100">{n.title}</p>
            <p className="text-neutral-300">{n.body}</p>
            <div className="text-xs text-neutral-300"><Links links={n.links} /></div>
          </div>
        ))}
      </div>
    </Card>
  );
}
