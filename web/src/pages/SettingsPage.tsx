import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { backend, type ImportResult } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";

export default function SettingsPage() {
  const health = useQuery({ queryKey: ["health"], queryFn: () => backend.health() });
  const vehicles = useQuery({ queryKey: ["vehicles"], queryFn: () => backend.vehicles() });

  return (
    <div className="space-y-4">
      <PageHeader title="Settings" />

      <Card title="Backend">
        {health.isLoading ? (
          <Spinner />
        ) : health.isError ? (
          <ErrorBox title="Backend unreachable" detail={String(health.error)} />
        ) : (
          <dl className="text-sm grid grid-cols-[auto,1fr] gap-x-4 gap-y-1">
            <dt className="text-neutral-500">Version</dt>
            <dd className="text-neutral-200">{health.data?.version}</dd>
            <dt className="text-neutral-500">Server time</dt>
            <dd className="text-neutral-200">{health.data?.time}</dd>
          </dl>
        )}
      </Card>

      <Card title="Import ElectraFi CSV">
        <ImportPanel />
      </Card>

      <Card title="Vehicles">
        {vehicles.isLoading ? (
          <Spinner />
        ) : vehicles.isError ? (
          <ErrorBox title="Failed to load vehicles" detail={String(vehicles.error)} />
        ) : !vehicles.data || vehicles.data.length === 0 ? (
          <div className="text-sm text-neutral-400">
            <p>No vehicles connected yet.</p>
            <p className="mt-1 text-xs text-neutral-500">
              Rivian account linking is not yet implemented. In the meantime,
              drop an ElectraFi CSV export into the Import panel above.
            </p>
          </div>
        ) : (
          <ul className="text-sm divide-y divide-neutral-800">
            {vehicles.data.map((v) => (
              <li key={v.id} className="py-2 flex justify-between">
                <span className="text-neutral-200">{v.name || v.vin}</span>
                <span className="text-neutral-500">{v.model}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card title="Notifications">
        <p className="text-sm text-neutral-400">
          Push notifications (charging complete, plug-in reminders, anomaly alerts)
          will land once the Rivian ingester is wired. The server-side VAPID keypair
          is already generated and persisted.
        </p>
      </Card>
    </div>
  );
}

// ImportPanel lets the user drop or pick ElectraFi CSV exports and
// streams them straight to POST /api/import/electrafi. On success we
// invalidate the cached drives/charges/samples so the rest of the app
// reflects the new data without a reload.
function ImportPanel() {
  const qc = useQueryClient();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const [dragging, setDragging] = useState(false);
  const [packKWh, setPackKWh] = useState<string>("141.5");

  const mut = useMutation({
    mutationFn: (files: File[]) =>
      backend.importElectrafi(files, Number(packKWh) || undefined),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["drives"] });
      qc.invalidateQueries({ queryKey: ["charges"] });
      qc.invalidateQueries({ queryKey: ["samples"] });
    },
  });

  const handleFiles = (fl: FileList | null) => {
    if (!fl || fl.length === 0) return;
    const files = Array.from(fl).filter((f) => /\.csv$/i.test(f.name));
    if (files.length === 0) return;
    mut.mutate(files);
  };

  const results: ImportResult[] = mut.data?.files ?? [];

  return (
    <div className="space-y-3">
      <div
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          handleFiles(e.dataTransfer.files);
        }}
        onClick={() => inputRef.current?.click()}
        className={`rounded-xl border-2 border-dashed p-6 text-center cursor-pointer transition-colors ${
          dragging
            ? "border-emerald-400 bg-emerald-500/5"
            : "border-neutral-700 hover:border-neutral-600"
        }`}
      >
        <input
          ref={inputRef}
          type="file"
          accept=".csv,text/csv"
          multiple
          className="hidden"
          onChange={(e) => {
            handleFiles(e.target.files);
            e.target.value = "";
          }}
        />
        <div className="text-sm text-neutral-300">
          {mut.isPending ? (
            <span className="inline-flex items-center gap-2">
              <Spinner /> Importing…
            </span>
          ) : (
            <>
              <span className="font-medium text-neutral-200">Drop CSV files here</span>
              <span className="text-neutral-500"> or click to browse</span>
            </>
          )}
        </div>
        <div className="mt-1 text-xs text-neutral-500">
          ElectraFi / TeslaFi exports. Multiple files OK.
        </div>
      </div>

      <div className="flex items-center gap-2 text-xs text-neutral-400">
        <label htmlFor="pack-kwh" className="whitespace-nowrap">
          Pack capacity
        </label>
        <input
          id="pack-kwh"
          type="number"
          step="0.1"
          min="0"
          value={packKWh}
          onChange={(e) => setPackKWh(e.target.value)}
          className="w-20 rounded border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200 tabular-nums"
        />
        <span>kWh · used to estimate energy when ElectraFi omits <code>charger_power</code> (late-Mar 2026 onward)</span>
      </div>

      {mut.isError && (
        <ErrorBox title="Import failed" detail={String(mut.error)} />
      )}

      {results.length > 0 && (
        <div className="rounded-lg border border-neutral-800 overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-neutral-900 text-neutral-400 text-xs uppercase tracking-wide">
              <tr>
                <th className="text-left px-3 py-2">File</th>
                <th className="text-right px-3 py-2">Rows</th>
                <th className="text-right px-3 py-2">Samples</th>
                <th className="text-right px-3 py-2">Drives</th>
                <th className="text-right px-3 py-2">Charges</th>
                <th className="text-right px-3 py-2">Skipped</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-neutral-800">
              {results.map((r) => (
                <tr key={r.File}>
                  <td className="px-3 py-2 text-neutral-200 truncate max-w-[16rem]">
                    {r.File}
                  </td>
                  <td className="px-3 py-2 text-right text-neutral-300">{r.Rows}</td>
                  <td className="px-3 py-2 text-right text-neutral-300">{r.Samples}</td>
                  <td className="px-3 py-2 text-right text-neutral-300">{r.Drives}</td>
                  <td className="px-3 py-2 text-right text-neutral-300">{r.Charges}</td>
                  <td className="px-3 py-2 text-right text-neutral-500">
                    {r.SkippedRows}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
