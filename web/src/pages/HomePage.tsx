import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { backend, machine as machineApi, type ShotListItem, type ShotMetrics } from "../lib/api";
import Logo from "../components/Logo";
import { useLiveStatus } from "../lib/useLiveStatus";

// HomePage is intentionally dashboard-y: a short hero, live system tiles,
// the latest shots, and quick navigation shortcuts. Every tile links to
// the relevant detail view so the page stays useful after the first visit.
export default function HomePage() {
  const live = useQuery({
    queryKey: ["live-status"],
    queryFn: () => backend.liveStatus(),
    refetchInterval: 15_000,
  });
  const machine = useQuery({
    queryKey: ["machine-status"],
    queryFn: () => backend.machineStatus(),
    refetchInterval: 15_000,
  });
  const profiles = useQuery({
    queryKey: ["machine", "profiles"],
    queryFn: () => machineApi.listProfiles(),
    refetchInterval: 60_000,
    retry: false,
  });
  const shotsStatus = useQuery({
    queryKey: ["shots-status"],
    queryFn: () => backend.shotsStatus(),
    refetchInterval: 30_000,
  });
  const recentShots = useQuery({
    queryKey: ["shots", "recent"],
    queryFn: () => backend.listShots(3),
    refetchInterval: 30_000,
  });
  // Tiny metrics cache so the Recent-shots list can show yield/peak next
  // to the duration. Fetches the same batch the History page uses, so
  // the query is usually already warm.
  const recentMetrics = useQuery({
    queryKey: ["shots-metrics"],
    queryFn: () => backend.listShotMetrics(200, 24),
    refetchInterval: 30_000,
  });

  const latest = recentShots.data?.[0];

  return (
    // space-y-8 on desktop gives the dashboard room to breathe, but it
    // pushes the page past an iPad-landscape viewport (1024×768) and
    // forces scrolling. iPad hits Tailwind's lg breakpoint (1024px), so
    // keep the compact rhythm through lg and only go spacious at xl+.
    <div className="space-y-5 md:space-y-5 xl:space-y-8">
      <Hero latest={latest} />

      <section className="grid gap-3 md:gap-4 sm:grid-cols-3">
        <StatTile
          label="Machine"
          value={
            machine.data
              ? machine.data.reachable
                ? machine.data.degraded
                  ? "spotty"
                  : "online"
                : "unreachable"
              : machine.isLoading
                ? "…"
                : "unknown"
          }
          tone={
            machine.data
              ? machine.data.reachable
                ? machine.data.degraded
                  ? "warn"
                  : "ok"
                : "error"
              : "muted"
          }
          hint={
            machine.data?.degraded
              ? `${machine.data.machine_url} — retrying (${machine.data.attempts ?? 0} attempts)`
              : machine.data?.machine_url
                ? live.data?.connected
                  ? `${machine.data.machine_url} · live`
                  : machine.data.machine_url
                : undefined
          }
          to="/live"
        />
        <StatTile
          label="Profiles"
          value={
            profiles.data
              ? profiles.data.length.toLocaleString()
              : profiles.isLoading
                ? "…"
                : "—"
          }
          tone={profiles.data ? "info" : "muted"}
          hint={
            profiles.error
              ? "machine unreachable"
              : profiles.data
                ? "on the machine"
                : undefined
          }
          to="/profiles"
        />
        <StatTile
          label="Shots cached"
          value={shotsStatus.data ? shotsStatus.data.shots_cached.toLocaleString() : "…"}
          tone="info"
          hint={
            shotsStatus.data?.last_sync
              ? `last sync ${formatRelative(shotsStatus.data.last_sync)}`
              : undefined
          }
          to="/history"
        />
      </section>

      <section className="grid gap-4 lg:gap-6 lg:grid-cols-[2fr_1fr] lg:items-stretch">
        <RecentShots
          loading={recentShots.isLoading}
          shots={recentShots.data ?? []}
          metrics={recentMetrics.data}
        />
        <MachineStatusPanel />
      </section>
    </div>
  );
}

function Hero({ latest }: { latest: ShotListItem | undefined }) {
  return (
    // Padding and type scale step down from md through lg (iPad landscape
    // sits right at lg=1024px) so hero + tiles + cards fit above the fold.
    // sm: still generous for phones; xl: restores desktop breathing room.
    <section className="relative overflow-hidden rounded-xl border border-neutral-800 bg-gradient-to-br from-neutral-900 via-neutral-950 to-emerald-950/40 px-6 py-5 sm:px-10 sm:py-10 md:px-8 md:py-5 xl:py-12">
      {/* Decorative steam — sits behind the copy, emerald glow. */}
      <div className="pointer-events-none absolute -top-12 -right-10 opacity-20">
        <Logo size={260} className="text-emerald-400" />
      </div>
      <div className="relative max-w-2xl space-y-2 md:space-y-2 xl:space-y-4">
        <div className="inline-flex items-center gap-2 rounded-full border border-emerald-800/60 bg-emerald-900/20 px-3 py-1 text-xs font-medium text-emerald-300">
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
          Meticulous espresso companion
        </div>
        <h1 className="text-2xl sm:text-4xl md:text-2xl xl:text-4xl font-semibold tracking-tight text-neutral-50">
          Pull better shots.{" "}
          <span className="text-emerald-300 xl:block">Dial in with data.</span>
        </h1>
        <p className="hidden xl:block text-sm sm:text-base leading-relaxed text-neutral-400">
          Live pressure, flow, and weight from your machine. Full shot history
          with charts. AI-powered analysis of each extraction. Everything stays
          on your network.
        </p>
        <p className="block xl:hidden text-sm leading-snug text-neutral-400">
          Live pressure, flow, and weight. Full shot history with charts.
          AI-powered analysis. Everything on your network.
        </p>
        <div className="flex flex-wrap gap-2 md:gap-2 xl:gap-3 pt-1 xl:pt-2">
          <Link
            to="/live"
            className="rounded-md border border-emerald-800 bg-emerald-900/40 px-4 py-2 text-sm font-medium text-emerald-200 transition hover:bg-emerald-900/70 hover:text-emerald-100"
          >
            Live shot →
          </Link>
          {latest && (
            <Link
              to={`/history/${encodeURIComponent(latest.id)}`}
              className="rounded-md border border-neutral-700 bg-neutral-900/60 px-4 py-2 text-sm text-neutral-200 transition hover:bg-neutral-800"
            >
              Last shot: {latest.name || "unnamed"}
            </Link>
          )}
        </div>
      </div>
    </section>
  );
}

type Tone = "ok" | "warn" | "error" | "info" | "muted";

function StatTile({
  label,
  value,
  tone,
  hint,
  to,
}: {
  label: string;
  value: string;
  tone: Tone;
  hint?: string;
  // Optional: when omitted the tile is not clickable. Some stats (e.g.
  // "Machine online") don't correspond to a settable preference, so
  // linking to /settings just dumps the user on an unrelated page.
  to?: string;
}) {
  const toneClass: Record<Tone, string> = {
    ok: "text-emerald-300",
    warn: "text-amber-300",
    error: "text-rose-300",
    info: "text-neutral-50",
    muted: "text-neutral-400",
  };
  const dotClass: Record<Tone, string> = {
    ok: "bg-emerald-400",
    warn: "bg-amber-400",
    error: "bg-rose-400",
    info: "bg-sky-400",
    muted: "bg-neutral-600",
  };
  const body = (
    <>
      <div className="flex items-center justify-between text-[11px] font-medium uppercase tracking-wider text-neutral-500">
        <span>{label}</span>
        <span className={`h-2 w-2 rounded-full ${dotClass[tone]}`} />
      </div>
      <div className={`mt-2 font-mono text-xl font-medium tabular-nums ${toneClass[tone]}`}>
        {value}
      </div>
      {hint && (
        <div className="mt-1 truncate text-xs text-neutral-500" title={hint}>
          {hint}
        </div>
      )}
    </>
  );
  const base = "rounded-lg border border-neutral-800 bg-neutral-950/60 p-4";
  if (!to) {
    return <div className={base}>{body}</div>;
  }
  return (
    <Link
      to={to}
      className={`${base} group block transition hover:border-neutral-700 hover:bg-neutral-900`}
    >
      {body}
    </Link>
  );
}

function RecentShots({
  loading,
  shots,
  metrics,
}: {
  loading: boolean;
  shots: ShotListItem[];
  metrics?: Record<string, ShotMetrics>;
}) {
  return (
    <section className="flex h-full flex-col rounded-lg border border-neutral-800 bg-neutral-950/60">
      <header className="flex items-center justify-between border-b border-neutral-800 px-4 py-3">
        <h2 className="text-sm font-semibold text-neutral-200">Recent shots</h2>
        <Link to="/history" className="text-xs text-neutral-400 hover:text-neutral-200">
          View all →
        </Link>
      </header>
      {loading ? (
        <p className="px-4 py-6 text-sm text-neutral-500">Loading…</p>
      ) : shots.length === 0 ? (
        <p className="px-4 py-6 text-sm text-neutral-500">
          No shots cached yet. Pull one on the machine and sync from the History page.
        </p>
      ) : (
        <ul className="divide-y divide-neutral-800">
          {shots.map((s) => {
            const m = metrics?.[s.id];
            return (
            <li key={s.id}>
              <Link
                to={`/history/${encodeURIComponent(s.id)}`}
                className="flex items-center justify-between gap-4 px-4 py-2.5 md:py-2 xl:py-3 transition hover:bg-neutral-900"
              >
                <div className="min-w-0">
                  <div className="truncate text-sm text-neutral-100">{s.name || "(unnamed)"}</div>
                  <div className="truncate text-xs text-neutral-500">
                    {/* shot.name is usually the profile name on Meticulous
                        firmware — only show the profile breadcrumb when it
                        actually adds information. */}
                    {s.profile_name && s.profile_name !== s.name
                      ? `${s.profile_name} · `
                      : ""}
                    {formatShotStats(s.sample_count, m)}
                  </div>
                </div>
                <div className="shrink-0 text-right font-mono text-xs text-neutral-400">
                  {formatRelative(new Date(s.time * 1000).toISOString())}
                </div>
              </Link>
            </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

// MachineStatusPanel subscribes to the live WebSocket and shows the most
// recent sensor snapshot — state, temperature, pressure, flow, weight,
// piston position. Replaces the static "quick actions" panel with
// something dashboard-y that updates in real time.
// PREHEAT_CYCLE_MS is the upper bound we use to decide whether a preheat
// is still in progress. Meticulous doesn't expose a "heating" flag on
// /status, so we combine last_triggered with the live temperature: if
// the machine was told to preheat recently AND the group is still cold,
// it's almost certainly still warming up.
const PREHEAT_CYCLE_MS = 10 * 60 * 1000;
const PREHEAT_READY_TEMP_C = 85;

function MachineStatusPanel() {
  const { status } = useLiveStatus();
  const qc = useQueryClient();
  const preheatStatus = useQuery({
    queryKey: ["preheat-status"],
    queryFn: () => backend.preheatStatus(),
    refetchInterval: 60_000,
  });
  // Tick every 15s while a preheat could be in progress so the badge's
  // remaining time updates and it disappears on its own.
  const [now, setNow] = useState(() => Date.now());
  const lastTriggeredMs = preheatStatus.data?.last_triggered
    ? Date.parse(preheatStatus.data.last_triggered)
    : NaN;
  const preheatElapsedMs =
    Number.isFinite(lastTriggeredMs) ? now - lastTriggeredMs : Infinity;
  const temp = status?.sensors.t;
  const withinWindow =
    preheatElapsedMs >= 0 && preheatElapsedMs < PREHEAT_CYCLE_MS;
  // Only show "Preheating" if the machine is still actually cold.
  // A recent last_triggered on an already-hot machine means we're done,
  // not mid-cycle.
  const preheating =
    withinWindow && (temp === undefined || temp < PREHEAT_READY_TEMP_C);
  useEffect(() => {
    if (!preheating) return;
    const id = window.setInterval(() => setNow(Date.now()), 5_000);
    return () => window.clearInterval(id);
  }, [preheating]);
  const [preheatFlash, setPreheatFlash] = useState<null | "ok" | "err">(null);
  const [tareFlash, setTareFlash] = useState<null | "ok" | "err">(null);
  const preheat = useMutation({
    mutationFn: () => backend.triggerPreheat(),
    onSuccess: () => {
      setPreheatFlash("ok");
      qc.invalidateQueries({ queryKey: ["preheat-status"] });
      setTimeout(() => setPreheatFlash(null), 1500);
    },
    onError: () => {
      setPreheatFlash("err");
      setTimeout(() => setPreheatFlash(null), 2500);
    },
  });
  const tare = useMutation({
    mutationFn: () => machineApi.triggerAction("tare"),
    onSuccess: () => {
      setTareFlash("ok");
      setTimeout(() => setTareFlash(null), 1500);
    },
    onError: () => {
      setTareFlash("err");
      setTimeout(() => setTareFlash(null), 2500);
    },
  });

  return (
    <section className="flex h-full flex-col rounded-lg border border-neutral-800 bg-neutral-950/60">
      <header className="flex items-center justify-between border-b border-neutral-800 px-4 py-3">
        <h2 className="text-sm font-semibold text-neutral-200">Machine status</h2>
        <Link
          to="/live"
          className="text-xs text-neutral-500 transition hover:text-neutral-300"
        >
          Live →
        </Link>
      </header>

      <div className="flex items-stretch divide-x divide-neutral-800 border-b border-neutral-800">
        <SensorStat
          label="Temp"
          value={status ? status.sensors.t.toFixed(1) : "—"}
          unit="°C"
          accent="text-orange-300"
        />
        <SensorStat
          label="Weight"
          value={status ? status.sensors.w.toFixed(1) : "—"}
          unit="g"
          accent="text-neutral-100"
        />
      </div>

      <div className="flex gap-2 border-b border-neutral-800 px-4 py-3">
        <ActionButton
          label="Preheat"
          pending={preheat.isPending}
          flash={preheatFlash}
          tone="orange"
          onClick={() => preheat.mutate()}
          active={preheating}
          activeLabel={preheating ? "Preheating" : undefined}
          progress={
            preheating
              ? Math.min(
                  1,
                  Math.max(0, preheatElapsedMs / PREHEAT_CYCLE_MS),
                )
              : undefined
          }
        />
        <ActionButton
          label="Tare"
          pending={tare.isPending}
          flash={tareFlash}
          tone="sky"
          onClick={() => tare.mutate()}
        />
      </div>

      <InfoRow
        to="/settings#preheat-schedule"
        label="Next preheat"
        value={
          preheatStatus.data?.next_scheduled
            ? formatNextPreheat(preheatStatus.data.next_scheduled)
            : "no schedule"
        }
        hint={
          preheatStatus.data?.next_scheduled
            ? formatCountdown(preheatStatus.data.next_scheduled)
            : "tap to add one"
        }
      />
    </section>
  );
}

function SensorStat({
  label,
  value,
  unit,
  accent,
}: {
  label: string;
  value: string;
  unit: string;
  accent: string;
}) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-1 bg-neutral-950/80 px-3 py-3">
      <div className="text-[10px] uppercase tracking-wider text-neutral-500">
        {label}
      </div>
      <div className="flex items-baseline gap-1">
        <span className={`font-mono text-xl tabular-nums ${accent}`}>{value}</span>
        <span className="text-[10px] text-neutral-500">{unit}</span>
      </div>
    </div>
  );
}

function ActionButton({
  label,
  pending,
  flash,
  tone,
  onClick,
  active = false,
  activeLabel,
  progress,
}: {
  label: string;
  pending: boolean;
  flash: null | "ok" | "err";
  tone: "orange" | "sky";
  onClick: () => void;
  // Used to surface an in-progress background state on the button
  // itself (e.g. "Preheating"). Stays clickable so the user can
  // re-trigger, unlike `pending` which disables the button.
  active?: boolean;
  activeLabel?: string;
  // Progress 0..1 rendered as a horizontal fill behind the label.
  // Omit to show just the active styling with no bar.
  progress?: number;
}) {
  const palette =
    tone === "orange"
      ? "border-orange-500/40 bg-orange-500/15 text-orange-200 hover:bg-orange-500/25 active:bg-orange-500/35"
      : "border-sky-500/40 bg-sky-500/15 text-sky-200 hover:bg-sky-500/25 active:bg-sky-500/35";
  const activeBoost =
    active && tone === "orange"
      ? "border-orange-400/70 text-orange-100 shadow-orange-500/20 shadow"
      : "";
  const text = pending
    ? "triggering…"
    : flash === "ok"
      ? "sent ✓"
      : flash === "err"
        ? "failed"
        : active && activeLabel
          ? activeLabel
          : label;
  const pct =
    typeof progress === "number"
      ? Math.round(Math.min(1, Math.max(0, progress)) * 100)
      : null;
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={pending}
      className={`relative flex-1 overflow-hidden rounded-md border px-4 py-2 text-sm font-semibold tabular-nums shadow-sm transition disabled:opacity-50 ${palette} ${activeBoost}`}
    >
      {pct !== null && (
        <span
          aria-hidden
          className="pointer-events-none absolute inset-y-0 left-0 bg-orange-500/35 transition-[width] duration-700 ease-linear"
          style={{ width: `${pct}%` }}
        />
      )}
      <span className="relative flex items-center justify-center gap-1.5">
        {active && !pending && !flash && (
          <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-orange-300" />
        )}
        {text}
      </span>
    </button>
  );
}

// InfoRow is a borderless tappable row used inside the Machine status card
// to surface bits of context (next preheat, last shot, …). Keeps the right
// column from collapsing into a stub when the action grid is short.
function InfoRow({
  to,
  label,
  value,
  hint,
}: {
  to: string;
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <Link
      to={to}
      className="block border-t border-neutral-800 px-4 py-2.5 transition hover:bg-neutral-900/60"
    >
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-[10px] uppercase tracking-wider text-neutral-500">
          {label}
        </span>
        <span className="truncate text-sm text-neutral-200" title={value}>
          {value}
        </span>
      </div>
      {hint && (
        <div className="mt-0.5 text-right text-[11px] text-neutral-500">{hint}</div>
      )}
    </Link>
  );
}

// "Mon 07:30" if it's not today, "today 07:30" / "tomorrow 07:30" otherwise.
function formatNextPreheat(iso: string): string {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return iso;
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  const tomorrow = new Date(now);
  tomorrow.setDate(now.getDate() + 1);
  const isTomorrow =
    d.getFullYear() === tomorrow.getFullYear() &&
    d.getMonth() === tomorrow.getMonth() &&
    d.getDate() === tomorrow.getDate();
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (sameDay) return `today ${time}`;
  if (isTomorrow) return `tomorrow ${time}`;
  return `${d.toLocaleDateString([], { weekday: "short" })} ${time}`;
}

// "in 14h 26m" / "in 3m" countdown to a future ISO timestamp.
function formatCountdown(iso: string): string {
  const then = new Date(iso).getTime();
  if (!Number.isFinite(then)) return "";
  const ms = then - Date.now();
  if (ms <= 0) return "now";
  const mins = Math.round(ms / 60_000);
  if (mins < 60) return `in ${mins}m`;
  const hrs = Math.floor(mins / 60);
  const rem = mins % 60;
  if (hrs < 24) return rem ? `in ${hrs}h ${rem}m` : `in ${hrs}h`;
  const days = Math.floor(hrs / 24);
  const remH = hrs % 24;
  return remH ? `in ${days}d ${remH}h` : `in ${days}d`;
}

// Shot duration, derived from sample count (Meticulous firmware samples
// sensors at ~10 Hz). "24.3s" is friendlier than "243 samples".
function formatShotDuration(sampleCount: number): string {
  if (!sampleCount) return "0.0s";
  return `${(sampleCount / 10).toFixed(1)}s`;
}

// Compact headline used in the Recent-shots list: duration plus yield
// and peak pressure when the server has them. Zero / missing values
// are skipped.
function formatShotStats(
  sampleCount: number,
  m: { peak_pressure?: number; final_weight?: number } | undefined,
): string {
  const parts: string[] = [formatShotDuration(sampleCount)];
  if (m?.final_weight && m.final_weight > 0) {
    parts.push(`${m.final_weight.toFixed(1)}g`);
  }
  if (m?.peak_pressure && m.peak_pressure > 0) {
    parts.push(`${m.peak_pressure.toFixed(1)} bar`);
  }
  return parts.join(" · ");
}

// Short "3m ago" / "2h ago" / "yesterday" / locale-date formatter. Avoids a
// library and stays small — good enough for a home-page list.
function formatRelative(iso: string): string {
  const now = Date.now();
  const then = new Date(iso).getTime();
  if (!Number.isFinite(then)) return "";
  const deltaSecs = Math.round((now - then) / 1000);
  if (deltaSecs < 0) return "just now";
  if (deltaSecs < 60) return `${deltaSecs}s ago`;
  const m = Math.round(deltaSecs / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 7) return `${d}d ago`;
  return new Date(iso).toLocaleDateString();
}
