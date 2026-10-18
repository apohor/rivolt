import { stopImpersonation, useImpersonationTarget } from "../lib/impersonation";

// ImpersonationBanner is the persistent, impossible-to-miss
// indicator that an admin is viewing the app as someone else. Exit
// clears the sessionStorage flag and forces a full page reload —
// see startImpersonation's doc for why that's a hard requirement,
// not a style choice.
export function ImpersonationBanner() {
  const target = useImpersonationTarget();
  if (!target) return null;
  return (
    <div className="sticky top-0 z-1200 flex items-center justify-center gap-3 bg-amber-900/95 px-4 py-1.5 text-xs text-amber-100">
      <span>
        Viewing as <strong>{target.email}</strong> — read-only
      </span>
      <button
        type="button"
        onClick={() => {
          stopImpersonation();
          window.location.assign("/admin");
        }}
        className="rounded-full border border-amber-200/50 px-2 py-0.5 font-medium hover:bg-amber-800/60"
      >
        Exit
      </button>
    </div>
  );
}
