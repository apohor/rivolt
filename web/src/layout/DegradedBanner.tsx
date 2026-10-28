import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { expiryNotice } from "../lib/rivianExpiry";

// The tab-bar shell dropped the header's connection pill, which was the
// only signal for "backend unreachable / Rivian not connected / MFA
// pending / needs re-auth". This banner returns that signal, but only
// when something is actually wrong; healthy renders nothing. It reads
// the same sources the pill did (/api/health, /api/settings/rivian/).

export type Degraded = { tone: "rose" | "amber"; text: string; to?: string; cta?: string };

export function degradedState(
  health: { ok?: boolean } | undefined,
  healthError: boolean,
  rivian:
    | { enabled: boolean; authenticated: boolean; mfa_pending: boolean; needs_reauth?: boolean; paused?: boolean; session_days_left?: number; session_expires_at?: string }
    | undefined,
): Degraded | null {
  if (healthError) {
    return { tone: "rose", text: "Rivolt can't reach its server. Data below may be out of date." };
  }
  if (!health?.ok || !rivian) return null; // still loading: say nothing
  if (!rivian.enabled) return null; // stub build: nothing to connect
  // The operator's pause beats every per-user state: nothing the user
  // does (re-auth, a code) will help until it's lifted.
  if (rivian.paused) {
    return {
      tone: "amber",
      text: "Rivolt has paused its connection to Rivian. Your history is safe; live updates will resume.",
    };
  }
  if (rivian.mfa_pending) {
    return { tone: "amber", text: "Rivian is waiting for your verification code.", to: "/settings?tab=account", cta: "Enter code" };
  }
  if (rivian.needs_reauth) {
    return { tone: "amber", text: "Your Rivian sign-in expired. Reconnect to keep recording.", to: "/settings?tab=account", cta: "Reconnect" };
  }
  if (!rivian.authenticated) {
    return { tone: "amber", text: "Rivian isn't connected.", to: "/settings?tab=account", cta: "Connect" };
  }
  // From a week before Rivian ends this sign-in: the date, and one tap to sign in again (the renewal).
  const notice = expiryNotice(rivian.session_days_left, rivian.session_expires_at);
  if (notice) return { tone: "amber", text: notice, to: "/settings?tab=account", cta: "Reconnect" };
  return null;
}

export default function DegradedBanner() {
  const health = useQuery({
    // Own key: other observers of ["health"] use default retries, which
    // would delay surfacing an outage.
    queryKey: ["health", "banner"],
    queryFn: () => backend.health(),
    refetchInterval: 15_000,
    // Fail fast: the default three backed-off retries would hide an
    // outage for several seconds.
    retry: 1,
    retryDelay: 500,
  });
  const rivian = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    refetchInterval: 15_000,
    enabled: !!health.data?.ok,
  });
  const d = degradedState(health.data, health.isError, rivian.data);
  if (!d) return null;
  const cls =
    d.tone === "rose"
      ? "border-rose-500/40 bg-rose-950/30 text-rose-200"
      : "border-amber-500/30 bg-amber-950/20 text-amber-200";
  return (
    <div role="status" className={`mb-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2 text-sm ${cls}`}>
      <span>{d.text}</span>
      {d.to && (
        <Link to={d.to} className="shrink-0 font-medium underline underline-offset-2">
          {d.cta}
        </Link>
      )}
    </div>
  );
}
