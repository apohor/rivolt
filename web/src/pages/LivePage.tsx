// Live espresso shot view.
//
// Opens a WebSocket to /api/live/ws and streams the machine's "status"
// and "sensors" events into a ring-buffered uPlot chart. We only push
// samples to the chart while the machine is actively extracting so the
// graph represents the current shot, not idle chatter.
import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery } from "@tanstack/react-query";
import ShotChart from "../components/ShotChart";
import { machine, MachineError } from "../lib/api";
import type { AlignedData } from "uplot";

const RING = 1200; // ~2 minutes @ 10 Hz; plenty for a shot

// Machine states that mean "this shot is over — anything the pump does
// from here on (purge, retract, cleanup) is not part of the espresso
// and must not be plotted or analyzed". Matched case-insensitively so
// firmware revisions that rename 'end' → 'finished' etc. still trip it.
const ENDED_STATES = new Set([
  "purge",
  "end",
  "ended",
  "finished",
  "done",
  "retract",
  "retracting",
  "idle",
]);

// Phase classification of the raw machine state string. The machine
// firmware doesn't use a single "purge" state — it emits "retracting"
// during cleanup and then "idle" once fully done. We collapse the
// cleanup states into a "post" phase so the UI can show one clear
// Purging indicator, and keep plain idle as its own neutral phase.
type Phase = "idle" | "extracting" | "post";
const POST_STATES = new Set([
  "purge",
  "end",
  "ended",
  "finished",
  "done",
  "retract",
  "retracting",
]);
function phaseOf(raw: string | undefined): Phase {
  const st = (raw || "").toLowerCase();
  if (!st || st === "idle") return "idle";
  if (POST_STATES.has(st)) return "post";
  return "extracting";
}

// Humanize raw machine state strings for display. Most firmware values
// are already title-case ("Pressure Hold"), but a few ("brewing",
// "retracting", "idle") look out of place next to them. This is purely
// cosmetic.
function stateLabel(raw: string | undefined): string {
  if (!raw) return "—";
  const st = raw.toLowerCase();
  if (st === "retracting" || st === "retract") return "Purging";
  if (st === "purge") return "Purging";
  if (st === "idle") return "Idle";
  if (st === "brewing") return "Brewing";
  if (st === "end" || st === "ended" || st === "finished" || st === "done")
    return "Done";
  return raw;
}

type StatusEvent = {
  name: string;
  sensors: { p: number; f: number; w: number; t: number; g: number };
  time: number;
  profile: string;
  profile_time: number;
  state: string;
  extracting: boolean;
  loaded_profile: string;
  id: string;
};

type LiveState = {
  connected: boolean;
  last_connect: string;
  last_error?: string;
  machine_url: string;
};

type Frame = {
  type: "state" | "event" | "ping";
  name?: string;
  data?: unknown;
  state?: LiveState;
};

// Pick the websocket URL relative to the current origin, so this works
// both in the embedded build (same origin) and behind the Vite proxy.
function wsURL(path: string) {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${location.host}${path}`;
}

export default function LivePage() {
  const [state, setState] = useState<LiveState | null>(null);
  const [last, setLast] = useState<StatusEvent | null>(null);
  const [connected, setConnected] = useState(false);
  // Keep the latest shot id we plotted so a new shot clears the buffer.
  const shotIdRef = useRef<string | null>(null);
  // Once a shot ends (purge/retract/idle/extracting→false after samples),
  // freeze the buffer for that id — don't append purge samples.
  const frozenRef = useRef(false);
  // After we freeze a shot with enough samples, remember its id so we can
  // render the AI analysis panel. The server-side recorder saves + auto-
  // analyzes on the same transition; the panel polls until ready.
  // Must be >= internal/live.MinSamplesToSave (currently 20) or the
  // backend will drop the shot and the panel would poll forever.
  const [endedShotId, setEndedShotId] = useState<string | null>(null);

  // Ring buffers. Pre-allocated; subarray()'d into uPlot without copying.
  // Sensors: p=pressure (bar), f=pump flow (ml/s), w=weight (g),
  // g=gravimetric flow (g/s, derived by the machine from the scale),
  // k=group temperature (°C). Single letter names so the rAF-hot path
  // stays readable.
  const bufRef = useRef({
    t: new Float64Array(RING),
    p: new Float64Array(RING),
    f: new Float64Array(RING),
    w: new Float64Array(RING),
    g: new Float64Array(RING),
    k: new Float64Array(RING),
    n: 0,
  });
  // Coalesce paints to one per animation frame. setTick at >30Hz makes
  // React + uPlot work overtime; rAF gives us smooth ~60fps redraws.
  const dirtyRef = useRef(false);
  const rafRef = useRef(0);
  const [tick, setTick] = useState(0);
  const schedulePaint = () => {
    if (dirtyRef.current) return;
    dirtyRef.current = true;
    rafRef.current = requestAnimationFrame(() => {
      dirtyRef.current = false;
      setTick((x) => x + 1);
    });
  };

  useEffect(() => {
    return () => cancelAnimationFrame(rafRef.current);
  }, []);

  useEffect(() => {
    const ws = new WebSocket(wsURL("/api/live/ws"));
    let closed = false;

    ws.onopen = () => setConnected(true);
    ws.onclose = () => {
      if (!closed) setConnected(false);
    };
    ws.onerror = () => setConnected(false);
    ws.onmessage = (ev) => {
      let frame: Frame;
      try {
        frame = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (frame.type === "state" && frame.state) {
        setState(frame.state);
        return;
      }
      if (frame.type !== "event") return;
      if (frame.name === "status" && frame.data) {
        const s = frame.data as StatusEvent;
        setLast(s);
        // New shot → reset buffer and freeze flag.
        if (s.id !== shotIdRef.current) {
          shotIdRef.current = s.id;
          bufRef.current.n = 0;
          frozenRef.current = false;
          // Clear the previous analysis panel; a new one will appear
          // when this shot ends with enough samples.
          setEndedShotId(null);
        }
        // Mark the shot as ended the first time we see a terminal
        // machine state (purge/retract/idle/end) OR we'd already
        // captured samples and `extracting` has flipped false. After
        // that, ignore further samples for this id so the chart shows
        // only the espresso, not the purge that follows.
        const st = (s.state || "").toLowerCase();
        if (
          !frozenRef.current &&
          (ENDED_STATES.has(st) ||
            (bufRef.current.n > 0 && !s.extracting))
        ) {
          frozenRef.current = true;
          // Only surface the analysis panel if the shot was long enough
          // for the backend to actually persist it (see MinSamplesToSave).
          if (bufRef.current.n >= 20 && shotIdRef.current) {
            setEndedShotId(shotIdRef.current);
          }
        }
        if (s.extracting && !frozenRef.current) {
          const b = bufRef.current;
          if (b.n < RING) {
            const i = b.n;
            // profile_time arrives in milliseconds; chart in seconds.
            // Use ?? not || — profile_time === 0 at the start of a shot
            // and || would fall back to wall-clock time.
            b.t[i] = (s.profile_time ?? s.time) / 1000;
            b.p[i] = s.sensors.p;
            b.f[i] = s.sensors.f;
            b.w[i] = s.sensors.w;
            b.g[i] = s.sensors.g;
            b.k[i] = s.sensors.t;
            b.n = i + 1;
            schedulePaint();
          }
        }
      }
    };
    return () => {
      closed = true;
      ws.close();
    };
  }, []);

  const data: AlignedData = useMemo(() => {
    const b = bufRef.current;
    const len = Math.min(b.n, RING);
    // Zero-copy: uPlot accepts typed-array subarrays. No allocation per frame.
    return [
      b.t.subarray(0, len),
      b.p.subarray(0, len),
      b.f.subarray(0, len),
      b.g.subarray(0, len),
      b.w.subarray(0, len),
      b.k.subarray(0, len),
    ] as unknown as AlignedData;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick]);

  const hasSamples = bufRef.current.n > 0;
  // Phase chip display. Trust the raw machine state in almost every
  // case — the firmware's own "brewing" / "idle" / "Pressure Hold" /
  // "Preinfusion" etc. strings are what the operator expects to see,
  // including the pre-shot "brewing" state the machine enters the
  // moment a new profile is loaded. The one case where we override it
  // is when *this particular shot* has already been recorded and
  // frozen — then the chart is showing historical data for a shot
  // that's already over, and leaving the chip on "brewing" while the
  // machine lingers in pressure-hold would be misleading.
  const rawPhase = phaseOf(last?.state);
  const shotOver = frozenRef.current;
  const phase: Phase =
    rawPhase === "extracting" && shotOver ? "post" : rawPhase;
  // Only override the label when we clamped the phase. Otherwise show
  // the raw firmware state (humanised) so "brewing" / "idle" /
  // "Pressure Hold" all come through accurately.
  const chipLabel = shotOver && rawPhase === "extracting"
    ? "Done"
    : stateLabel(last?.state);
  // The machine keeps profile_time ticking through purge/retract, so by
  // the time a 26s shot finishes the raw counter might read 70s. Clamp
  // the displayed value: while extracting, use the live value; after
  // freeze, pin to the last-sampled profile_time (= true shot duration);
  // with no samples, show 0.
  const displayProfileSeconds =
    phase === "extracting" && last
      ? last.profile_time / 1000
      : hasSamples
        ? bufRef.current.t[bufRef.current.n - 1]
        : 0;

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Live shot</h1>
          <p className="text-sm text-neutral-400">
            Streaming from the machine in real time.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <PhaseChip phase={phase} label={chipLabel} />
          <ConnectionPill connected={connected} state={state} />
        </div>
      </header>

      <ControlStrip
        loadedProfile={last?.loaded_profile}
        // Disable Start only while the pump is *actually* extracting.
        // The firmware's `extracting` boolean is false during the
        // pre-shot "brewing" state (which is where the machine sits
        // right after a profile is loaded), so using it here avoids
        // greying out Start the instant you pick a profile.
        extracting={!!last?.extracting && !frozenRef.current}
      />

      <section className="grid gap-3 sm:grid-cols-5">
        <Stat
          label="Pressure"
          value={last ? `${last.sensors.p.toFixed(1)} bar` : "—"}
        />
        <Stat
          label="Pump flow"
          value={last ? `${last.sensors.f.toFixed(1)} ml/s` : "—"}
        />
        <Stat
          label="Yield"
          value={last ? `${last.sensors.g.toFixed(1)} g/s` : "—"}
        />
        <Stat
          label="Weight"
          value={last ? `${last.sensors.w.toFixed(1)} g` : "—"}
        />
        <Stat
          label="Temp"
          value={last ? `${last.sensors.t.toFixed(1)} °C` : "—"}
        />
      </section>

      <section className="rounded-lg border border-neutral-800 bg-neutral-950 p-4">
        <div className="mb-3 flex items-center justify-between">
          <h2
            className={
              "font-medium transition-colors " +
              // Dim the profile banner when the machine is just sitting
              // idle (e.g. right after Exit/abort). The profile is still
              // technically "loaded" but nothing is happening, so the
              // full-brightness white label was misleading.
              (phase === "idle" ? "text-neutral-500" : "text-neutral-100")
            }
          >
            {last?.loaded_profile ?? "No profile"}
            {phase === "extracting" && (
              <span
                className="ml-2 inline-block h-2 w-2 animate-pulse rounded-full bg-rose-500 align-middle"
                title="Extracting"
              />
            )}
            {phase === "post" && (
              <span
                className="ml-2 inline-block h-2 w-2 animate-pulse rounded-full bg-orange-400 align-middle"
                title="Purging"
              />
            )}
          </h2>
          <span className="text-xs text-neutral-500">
            profile time {displayProfileSeconds.toFixed(1)}s
          </span>
        </div>
        {hasSamples ? (
          <ShotChart
            data={data}
            series={[
              { label: "Pressure", stroke: "#f97316", scale: "p", unit: "bar" },
              { label: "Pump flow", stroke: "#38bdf8", scale: "f", unit: "ml/s" },
              { label: "Yield", stroke: "#22d3ee", scale: "g", unit: "g/s" },
              { label: "Weight", stroke: "#a3a3a3", scale: "w", unit: "g" },
              // Group temperature — dashed so it's clearly a secondary
              // signal and doesn't compete visually with pressure/flow.
              {
                label: "Temp",
                stroke: "#f43f5e",
                scale: "k",
                unit: "°C",
                dash: [6, 4],
              },
            ]}
            scales={{
              x: { time: false },
              // Fixed ranges keep the axes from twitching every frame as
              // auto-bounds expand. Espresso-realistic windows.
              // Yield gets its own (taller) scale because the gravimetric
              // derivative is noisy and can briefly spike past pump flow.
              p: { auto: false, range: [0, 12] },
              f: { auto: false, range: [0, 10] },
              g: { auto: false, range: [0, 15] },
              w: { auto: false, range: [0, 60] },
              // Group temperature range: the machine streams the
              // heater-side reading which can briefly exceed 100°C
              // during preheat recovery and sits around 90–98°C during
              // extraction. A tight [80, 100] window clipped the line
              // at the top and bottom of the chart. Widen enough to
              // always contain the signal without collapsing the other
              // series against the baseline.
              k: { auto: false, range: [60, 120] },
            }}
            height={320}
          />
        ) : (
          <p className="py-12 text-center text-sm text-neutral-500">
            Waiting for a shot to start…
          </p>
        )}
      </section>

      {endedShotId && (
        <Link
          to={`/history/${encodeURIComponent(endedShotId)}`}
          className="flex items-center justify-between gap-4 rounded-lg border border-neutral-800 bg-neutral-950 px-4 py-3 transition hover:border-neutral-700 hover:bg-neutral-900"
        >
          <div className="min-w-0">
            <div className="text-xs uppercase tracking-wide text-neutral-500">
              Last shot
            </div>
            <div className="truncate text-sm text-neutral-200">
              {last?.loaded_profile || "Shot"} · open in history for AI analysis
            </div>
          </div>
          <span className="text-neutral-400">→</span>
        </Link>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-neutral-800 bg-neutral-950 px-4 py-3">
      <div className="text-xs uppercase tracking-wide text-neutral-500">
        {label}
      </div>
      <div className="mt-1 font-mono text-lg">{value}</div>
    </div>
  );
}

// ControlStrip groups the profile picker and the four most-used machine
// actions (Start / Stop / Purge / Raise) so the user can drive a shot
// without leaving the Live page. All calls go through the machine
// reverse-proxy at /api/machine/* — nothing to cache, nothing to sync
// locally. Errors surface as a small inline banner rather than a toast
// so they stick around long enough to read.
function ControlStrip({
  loadedProfile,
  extracting,
}: {
  loadedProfile?: string;
  extracting: boolean;
}) {
  const profiles = useQuery({
    queryKey: ["machine", "profiles"],
    queryFn: () => machine.listProfiles(),
    // Profiles rarely change during a session; 60s is plenty and
    // avoids hammering the machine on every render.
    staleTime: 60_000,
  });

  const [error, setError] = useState<string | null>(null);
  const clearError = () => setError(null);
  const handleError = (err: unknown) => {
    if (err instanceof MachineError) {
      setError(err.message);
    } else if (err instanceof Error) {
      setError(err.message);
    } else {
      setError(String(err));
    }
  };

  const load = useMutation({
    // The machine refuses profile/load with 409 "machine is busy"
    // whenever it's already sitting in a pre-shot "brewing" state
    // (which is where it goes the moment you pick any profile).
    // Switching profiles therefore needs to first exit that state
    // via action/stop, let the firmware settle, then load the new
    // one. action/stop is idempotent on an idle machine. If the load
    // still trips 409 we retry after a longer pause — the firmware
    // sometimes takes a moment after stop to accept a new load.
    mutationFn: async (id: string) => {
      try {
        await machine.triggerAction("stop");
      } catch {
        // ignored — machine may already be idle
      }
      // Give the firmware time to drop out of brewing. 250ms is enough
      // on idle, 700ms covers a fresh stop-then-load sequence.
      const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
      await sleep(300);
      try {
        return await machine.loadProfile(id);
      } catch (err) {
        if (err instanceof MachineError && err.status === 409) {
          await sleep(700);
          return await machine.loadProfile(id);
        }
        throw err;
      }
    },
    onMutate: clearError,
    onError: handleError,
  });
  const action = useMutation({
    mutationFn: (name: string) => machine.triggerAction(name),
    onMutate: clearError,
    onError: handleError,
  });

  // The Meticulous "loaded_profile" status field is a name string, not
  // an id — find the matching profile so the <select> can reflect it.
  // Falls back to "" (no selection) if the loaded profile has been
  // deleted since or the list hasn't landed yet.
  const currentId = useMemo(() => {
    if (!profiles.data || !loadedProfile) return "";
    return (
      profiles.data.find((p) => p.name === loadedProfile)?.id ?? ""
    );
  }, [profiles.data, loadedProfile]);

  return (
    <section className="rounded-lg border border-neutral-800 bg-neutral-950 p-3">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <label className="flex min-w-0 flex-1 items-center gap-2">
          <span className="shrink-0 text-xs uppercase tracking-wide text-neutral-500">
            Profile
          </span>
          <select
            value={currentId}
            disabled={profiles.isLoading || load.isPending}
            onChange={(e) => {
              const id = e.target.value;
              if (id) load.mutate(id);
            }}
            className="min-w-0 flex-1 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100 focus:border-emerald-600 focus:outline-none focus:ring-1 focus:ring-emerald-600"
          >
            {currentId === "" && (
              <option value="">
                {profiles.isLoading ? "loading…" : loadedProfile || "— pick a profile —"}
              </option>
            )}
            {(profiles.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </label>

        <div className="flex gap-2">
          <MachineButton
            label="Start"
            tone="emerald"
            pending={action.isPending && action.variables === "start"}
            disabled={extracting}
            onClick={() => action.mutate("start")}
          />
          <MachineButton
            label="Stop"
            tone="rose"
            pending={action.isPending && action.variables === "stop"}
            onClick={() => action.mutate("stop")}
          />
          <MachineButton
            label="Purge"
            tone="amber"
            pending={action.isPending && action.variables === "purge"}
            onClick={() => action.mutate("purge")}
          />
          <MachineButton
            label="Tare"
            tone="neutral"
            pending={action.isPending && action.variables === "tare"}
            onClick={() => action.mutate("tare")}
            title="Zero the built-in scale"
          />
          <MachineButton
            label="Raise"
            tone="sky"
            pending={action.isPending && action.variables === "home"}
            onClick={() => action.mutate("home")}
            title="Raise piston (home)"
          />
          <MachineButton
            label="Exit"
            tone="neutral"
            pending={action.isPending && action.variables === "abort"}
            onClick={() => action.mutate("abort")}
            title="Exit the loaded profile (abort)"
          />
        </div>
      </div>

      {error && (
        <div
          className="mt-2 rounded-md border border-rose-900/60 bg-rose-950/40 px-3 py-1.5 text-xs text-rose-200"
          role="alert"
        >
          {error}
        </div>
      )}
    </section>
  );
}

function MachineButton({
  label,
  tone,
  pending,
  disabled,
  onClick,
  title,
}: {
  label: string;
  tone: "emerald" | "rose" | "amber" | "sky" | "neutral";
  pending: boolean;
  disabled?: boolean;
  onClick: () => void;
  title?: string;
}) {
  const palette: Record<typeof tone, string> = {
    emerald:
      "border-emerald-600/50 bg-emerald-900/30 text-emerald-200 hover:bg-emerald-900/50",
    rose: "border-rose-700/50 bg-rose-950/40 text-rose-200 hover:bg-rose-950/70",
    amber:
      "border-amber-700/50 bg-amber-950/40 text-amber-200 hover:bg-amber-950/70",
    sky: "border-sky-700/50 bg-sky-950/40 text-sky-200 hover:bg-sky-950/70",
    neutral:
      "border-neutral-700 bg-neutral-900 text-neutral-200 hover:bg-neutral-800",
  };
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={pending || disabled}
      title={title}
      className={`rounded-md border px-3 py-1.5 text-xs font-semibold tabular-nums transition disabled:cursor-not-allowed disabled:opacity-40 ${palette[tone]}`}
    >
      {pending ? "…" : label}
    </button>
  );
}

function ConnectionPill({
  connected,
  state,
}: {
  connected: boolean;
  state: LiveState | null;
}) {
  const upstream = state?.connected ?? false;
  let label = "browser disconnected";
  let tone = "bg-neutral-800 text-neutral-400";
  if (connected && upstream) {
    label = "live";
    tone = "bg-emerald-900/40 text-emerald-300 border-emerald-800";
  } else if (connected && !upstream) {
    label = "machine offline";
    tone = "bg-amber-900/40 text-amber-300 border-amber-800";
  } else if (!connected) {
    label = "reconnecting…";
    tone = "bg-rose-900/40 text-rose-300 border-rose-800";
  }
  return (
    <span
      className={`rounded-full border border-neutral-800 px-3 py-1 text-xs ${tone}`}
      title={state?.last_error || state?.machine_url}
    >
      {label}
    </span>
  );
}

// PhaseChip surfaces the collapsed machine phase (extracting vs post-shot
// cleanup vs idle) with humanised labels. Lives in the Live header so the
// transition from extraction → purge is immediately visible instead of
// being buried as small text in a stat grid.
function PhaseChip({ phase, label }: { phase: Phase; label: string }) {
  const tone =
    phase === "extracting"
      ? "border-emerald-700 bg-emerald-900/40 text-emerald-200"
      : phase === "post"
        ? "border-orange-700 bg-orange-900/40 text-orange-200"
        : "border-neutral-800 bg-neutral-900 text-neutral-400";
  const dot =
    phase === "extracting"
      ? "bg-emerald-400 animate-pulse"
      : phase === "post"
        ? "bg-orange-400 animate-pulse"
        : "bg-neutral-600";
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium ${tone}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${dot}`} />
      {label}
    </span>
  );
}
