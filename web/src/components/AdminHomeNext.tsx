import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { agoFrom } from "../lib/adminFormat";
import { checks, formatAge, verdict, type Check, type HealthSummary, type Tone } from "../lib/adminHealth";
import { Card, ErrorBox, Spinner } from "./ui";

// Admin Home (new look): "Is everything OK?". One verdict up top, then the checks it is made of,
// then the detail behind each. Refreshes by itself; a section the server could not read says so.
const TONE: Record<Tone, { chip: string; text: string; border: string; word: string }> = {
  ok: { chip: "bg-emerald-500", text: "text-emerald-300", border: "border-emerald-700/60", word: "OK" },
  warn: { chip: "bg-amber-500", text: "text-amber-300", border: "border-amber-600/60", word: "Look" },
  bad: { chip: "bg-rose-500", text: "text-rose-300", border: "border-rose-600/60", word: "Act" },
  unknown: { chip: "bg-neutral-500", text: "text-neutral-300", border: "border-neutral-600", word: "Unknown" },
};

export function HomeNext({ goTo }: { goTo: (tab: "operations" | "users") => void }) {
  const q = useQuery({
    queryKey: ["admin", "health-summary"],
    queryFn: () => backend.adminHealthSummary(),
    refetchInterval: 30_000,
    retry: 1,
  });
  if (q.isLoading) return <Spinner />;
  if (q.isError || !q.data) return <ErrorBox title="Couldn't load the health summary" detail={String((q.error as Error | null)?.message ?? "")} />;
  const s = q.data;
  const cs = checks(s);
  const v = verdict(cs);
  return (
    <div data-admin-home className="space-y-4">
      <section aria-label="Overall" data-verdict={v.tone} className={`rounded-xl border p-4 ${TONE[v.tone].border} ${v.tone === "bad" ? "bg-rose-950/30" : v.tone === "warn" ? "bg-amber-950/20" : "bg-neutral-900/50"}`}>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className={`text-lg font-semibold ${TONE[v.tone].text}`}>{v.headline}</h2>
            <p className="mt-1 text-xs text-neutral-400">
              Checked {agoFrom(s.generated_at)} · Rivolt {s.version ? `v${s.version.replace(/^v/, "")}` : "version unknown"} · updates every 30 seconds
            </p>
          </div>
          <button type="button" onClick={() => void q.refetch()} disabled={q.isFetching} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50">
            {q.isFetching ? "Checking…" : "Refresh"}
          </button>
        </div>
        <ul className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {cs.map((c) => <CheckChip key={c.id} c={c} />)}
        </ul>
      </section>

      <div className="grid gap-4 lg:grid-cols-2">
        <AlertsCard s={s} />
        <RivianCard s={s} goTo={goTo} />
        <VehiclesCard s={s} goTo={goTo} />
        <InfraCard s={s} />
        <EngagementCard s={s} />
      </div>
    </div>
  );
}

function CheckChip({ c }: { c: Check }) {
  const t = TONE[c.tone];
  return (
    <li data-check={c.id} data-tone={c.tone} className={`flex items-center gap-3 rounded-lg border px-3 py-2 ${t.border}`}>
      <span aria-hidden className={`h-2.5 w-2.5 shrink-0 rounded-full ${t.chip}`} />
      <div className="min-w-0">
        <div className="text-sm font-medium text-neutral-100">{c.label} <span className={`text-xs font-normal ${t.text}`}>{t.word}</span></div>
        <div className="truncate text-xs text-neutral-400" title={c.detail}>{c.detail}</div>
      </div>
    </li>
  );
}

function Unavailable({ reason }: { reason?: string }) {
  return <p className="text-sm text-neutral-400">Unavailable{reason ? `: ${reason}` : ""}.</p>;
}

function AlertsCard({ s }: { s: HealthSummary }) {
  return (
    <Card title="Firing alerts">
      {s.alerts.status !== "ok" ? <Unavailable reason={s.alerts.reason} /> : s.alerts.items.length === 0 ? (
        <p className="text-sm text-emerald-300">No alerts firing.</p>
      ) : (
        <ul className="divide-y divide-neutral-800 text-sm">
          {s.alerts.items.map((a) => (
            <li key={`${a.name}-${a.since}`} className="flex items-baseline justify-between gap-3 py-2 first:pt-0">
              <span className="text-neutral-100">{a.name}</span>
              <span className="shrink-0 text-xs text-neutral-400">
                <span className={a.severity === "critical" || a.severity === "page" ? "text-rose-300" : "text-amber-300"}>{a.severity}</span> · {agoFrom(a.since)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function RivianCard({ s, goTo }: { s: HealthSummary; goTo: (t: "operations" | "users") => void }) {
  const r = s.rivian;
  const breaker = r.breaker_state === 2 ? "open" : r.breaker_state === 1 ? "half open" : r.breaker_state === 0 ? "closed" : "unknown";
  const classes = Object.entries(r.results ?? {}).sort((a, b) => b[1] - a[1]);
  const total = classes.reduce((n, [, c]) => n + c, 0);
  const sumOf = (m: Record<string, number> | undefined) => Object.values(m ?? {}).reduce((n, x) => n + x, 0);
  return (
    <Card title={`Rivian calls, last ${r.window || "24h"}`}>
      {r.status !== "ok" ? <Unavailable reason={r.reason} /> : (
        <div className="space-y-3 text-sm">
          <dl className="grid grid-cols-2 gap-x-4 gap-y-2">
            <Stat label="Calls" value={Math.round(total).toLocaleString()} />
            <Stat label="Error rate" value={r.error_rate == null ? "no calls" : `${(r.error_rate * 100).toFixed(1)}%`} />
            <Stat label="Breaker" value={breaker} />
            <Stat label="Breaker trips" value={String(Math.round(sumOf(r.breaker_trips)))} />
            <Stat label="Rate limited" value={String(Math.round(sumOf(r.rate_limited)))} />
          </dl>
          {classes.length > 0 && (
            <ul className="space-y-1 text-xs text-neutral-300">
              {classes.map(([k, c]) => (
                <li key={k} className="flex justify-between gap-3"><span>{k}</span><span className="tabular-nums text-neutral-400">{Math.round(c).toLocaleString()}</span></li>
              ))}
            </ul>
          )}
          <button type="button" onClick={() => goTo("operations")} className="text-xs text-emerald-300 underline underline-offset-2">Kill switch and flags</button>
        </div>
      )}
    </Card>
  );
}

function VehiclesCard({ s, goTo }: { s: HealthSummary; goTo: (t: "operations" | "users") => void }) {
  const v = s.vehicles;
  return (
    <Card title="Live vehicles">
      {v.status !== "ok" ? <Unavailable reason={v.reason} /> : (
        <div className="space-y-3 text-sm">
          <div className="text-2xl font-semibold tabular-nums text-neutral-100">
            {v.streaming} <span className="text-sm font-normal text-neutral-400">streaming of {v.leased} leased</span>
          </div>
          <p className="text-xs text-neutral-400">Leased: assigned to a pod to record. Streaming: wrote data in the last 10 minutes. A gap means something that should be recording is not.</p>
          <button type="button" onClick={() => goTo("users")} className="text-xs text-emerald-300 underline underline-offset-2">Find them under Users, No data</button>
        </div>
      )}
    </Card>
  );
}

function InfraCard({ s }: { s: HealthSummary }) {
  return (
    <Card title="Disk and backup">
      <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
        <div>
          <dt className="text-xs text-neutral-400">Database disk</dt>
          <dd className="font-semibold tabular-nums text-neutral-100">{s.disk.status === "ok" && s.disk.used_pct != null ? `${s.disk.used_pct.toFixed(0)}% used` : "Unavailable"}</dd>
          {s.disk.status !== "ok" && s.disk.reason && <dd className="text-xs text-neutral-500">{s.disk.reason}</dd>}
        </div>
        <div>
          <dt className="text-xs text-neutral-400">Last backup</dt>
          <dd className="font-semibold tabular-nums text-neutral-100">{s.backup.status === "ok" && s.backup.age_seconds != null ? `${formatAge(s.backup.age_seconds)} ago` : "Unavailable"}</dd>
          {s.backup.status !== "ok" && s.backup.reason && <dd className="text-xs text-neutral-500">{s.backup.reason}</dd>}
        </div>
      </dl>
    </Card>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-neutral-400">{label}</dt>
      <dd className="font-semibold tabular-nums text-neutral-100">{value}</dd>
    </div>
  );
}

const EMAIL_LABELS: [string, string][] = [
  ["welcome", "Welcome (28 d)"],
  ["setup_first", "Setup reminder 1 (ever)"],
  ["setup_followup", "Setup reminder 2 (ever)"],
  ["statement", "Statement (28 d)"],
];

function EngagementCard({ s }: { s: HealthSummary }) {
  const e = s.engagement;
  if (!e) return null;
  return (
    <Card title="Do people come back?">
      {e.status !== "ok" ? <Unavailable reason={e.reason} /> : (
        <div className="space-y-3 text-sm" data-engagement>
          <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
            <div><dt className="text-xs text-neutral-400">Seen in 7 days</dt><dd className="text-2xl font-semibold tabular-nums text-neutral-100">{e.active_7d}</dd></div>
            <div><dt className="text-xs text-neutral-400">Seen in 28 days</dt><dd className="text-2xl font-semibold tabular-nums text-neutral-100">{e.active_28d}</dd></div>
            <div><dt className="text-xs text-neutral-400">Connected, no data yet</dt><dd className="font-semibold tabular-nums text-neutral-100">{e.connected_no_data}</dd></div>
            <div><dt className="text-xs text-neutral-400">First data (7 / 28 d)</dt><dd className="font-semibold tabular-nums text-neutral-100">{e.first_data_7d} / {e.first_data_28d}</dd></div>
          </dl>
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs text-neutral-400">
            {EMAIL_LABELS.map(([k, label]) => (
              <div key={k} className="flex justify-between gap-2"><dt>{label}</dt><dd className="tabular-nums text-neutral-200">{e.emails_sent?.[k] ?? 0}</dd></div>
            ))}
          </dl>
          <p className="text-xs text-neutral-500">Seen means a signed-in session was used, not that the truck sent data. Numbers only, no accounts listed.</p>
        </div>
      )}
    </Card>
  );
}
