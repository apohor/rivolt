import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import type { AlignedData } from "uplot";
import { backend, MachineError, type Shot, type ShotSample } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import ShotChart from "../components/ShotChart";
import ShotAnalysisPanel from "../components/ShotAnalysisPanel";
import ShotCoachPanel from "../components/ShotCoachPanel";
import ShotComparePanel from "../components/ShotComparePanel";
import ShotFeedbackCard from "../components/ShotFeedbackCard";

export default function ShotDetailPage() {
  const { id = "" } = useParams();
  const { data, isLoading, error } = useQuery({
    queryKey: ["shot", id],
    queryFn: () => backend.getShot(id),
    enabled: !!id,
  });

  const chart = useMemo(() => (data ? buildChart(data.samples) : null), [data]);

  return (
    <div className="space-y-6">
      <PageHeader
        title={data?.name || "Shot"}
        subtitle={data ? formatHeader(data) : id}
        actions={
          <Link
            to="/history"
            className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800"
          >
            Back
          </Link>
        }
      />

      {isLoading && <Spinner />}
      {error && (
        <ErrorBox
          title="Could not load shot"
          detail={
            error instanceof MachineError
              ? `${error.status}: ${JSON.stringify(error.body)}`
              : String(error)
          }
        />
      )}

      {data && chart && (
        <>
          <Card title="Pressure · flow · weight">
            <ShotChart
              data={chart.data}
              series={chart.series}
              scales={chart.scales}
              height={320}
              zoomable
            />
          </Card>
          <Card title="Summary">
            <dl className="grid gap-2 text-sm md:grid-cols-3">
              <KV k="Samples" v={String(data.sample_count)} />
              <KV k="Duration (s)" v={chart.durationS.toFixed(1)} />
              <KV k="Peak pressure (bar)" v={chart.peak.pressure.toFixed(2)} />
              <KV k="Peak flow (ml/s)" v={chart.peak.flow.toFixed(2)} />
              <KV k="Final weight (g)" v={chart.peak.weight.toFixed(2)} />
              <KV k="Profile" v={data.profile_name || "(none)"} />
            </dl>
          </Card>
          <ShotFeedbackCard
            shotId={data.id}
            initialRating={data.rating ?? null}
            initialNote={data.note ?? ""}
            initialBeanId={data.bean_id ?? ""}
            initialGrind={data.grind ?? ""}
            initialGrindRPM={data.grind_rpm ?? null}
          />
          <ShotAnalysisPanel shotId={data.id} profileId={data.profile_id} />
          <ShotCoachPanel shotId={data.id} profileId={data.profile_id} />
          <ShotComparePanel shotId={data.id} profileId={data.profile_id} />
        </>
      )}
    </div>
  );
}

function KV({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-neutral-900/70 pb-1">
      <dt className="text-neutral-500">{k}</dt>
      <dd className="font-mono text-neutral-200">{v}</dd>
    </div>
  );
}

function formatHeader(s: Shot): string {
  const when = s.time ? new Date(s.time * 1000).toLocaleString() : "";
  const parts = [when, s.profile_name].filter(Boolean);
  return parts.join(" · ");
}

function buildChart(samples: ShotSample[]): {
  data: AlignedData;
  series: { label: string; stroke: string; scale?: string; unit?: string }[];
  scales: import("uplot").Options["scales"];
  peak: { pressure: number; flow: number; weight: number };
  durationS: number;
} {
  const n = samples.length;
  const x = new Float64Array(n);
  const pressure = new Float64Array(n);
  const flow = new Float64Array(n);
  const gravimetric = new Float64Array(n);
  const weight = new Float64Array(n);

  let peakP = 0;
  let peakF = 0;
  let finalW = 0;

  // Time units vary by source:
  //   - live-stream recorder persists `time` in ms and `profile_time` in ms
  //     (since shot start).
  //   - /api/v1/history upserts use the same ms convention.
  // Prefer profile_time (already zero-based) and fall back to `time`
  // normalized to seconds from the first sample. This matches the analyzer
  // and LivePage.
  const t0Profile = n > 0 ? numOrNaN(samples[0].profile_time) : NaN;
  const t0Time = n > 0 ? numOrNaN(samples[0].time) : NaN;
  const useProfile = !Number.isNaN(t0Profile);
  for (let i = 0; i < n; i++) {
    const s = samples[i];
    if (useProfile) {
      x[i] = (numOrNaN(s.profile_time) - t0Profile) / 1000;
    } else if (!Number.isNaN(t0Time)) {
      x[i] = (numOrNaN(s.time) - t0Time) / 1000;
    } else {
      x[i] = i;
    }
    const shot = s.shot ?? {};
    const p = numOrNaN(shot.pressure);
    const f = numOrNaN(shot.flow);
    const g = numOrNaN(shot.gravimetric_flow);
    const w = numOrNaN(shot.weight);
    pressure[i] = p;
    flow[i] = f;
    gravimetric[i] = g;
    weight[i] = w;
    if (!Number.isNaN(p) && p > peakP) peakP = p;
    if (!Number.isNaN(f) && f > peakF) peakF = f;
    if (!Number.isNaN(w)) finalW = w;
  }

  // uPlot expects primitive arrays of numbers (NaN permitted for gaps).
  const data: AlignedData = [
    Array.from(x),
    Array.from(pressure),
    Array.from(flow),
    Array.from(gravimetric),
    Array.from(weight),
  ];
  return {
    data,
    series: [
      { label: "pressure", stroke: "#f59e0b", scale: "p", unit: "bar" },
      { label: "pump flow", stroke: "#60a5fa", scale: "f", unit: "ml/s" },
      { label: "yield", stroke: "#22d3ee", scale: "f", unit: "g/s" },
      { label: "weight", stroke: "#34d399", scale: "w", unit: "g" },
    ],
    scales: {
      x: { time: false },
      p: { auto: true },
      f: { auto: true },
      w: { auto: true },
    },
    peak: { pressure: peakP, flow: peakF, weight: finalW },
    durationS: n > 0 ? x[n - 1] - x[0] : 0,
  };
}

function numOrNaN(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : NaN;
}
