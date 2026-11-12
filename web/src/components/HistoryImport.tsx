import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend, type ImportEvent, type ImportPreview, type ImportRecord, type ImportSampleRow } from "../lib/api";
import {
  MAX_IMPORT_FILES,
  MAX_IMPORT_MB,
  SOURCES,
  addedSummary,
  browserTZ,
  chargesText,
  drivesText,
  fmtDay,
  importFailure,
  previewEmpty,
  skipLabel,
  undoUnknown,
  undoWarning,
  warningText,
} from "../lib/historyImport";
import { Card, ErrorBox, Spinner } from "./ui";

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:cursor-not-allowed disabled:opacity-50";
const btnMain = "rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50";
const field = "rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100";

type Step = "source" | "files" | "preview" | "importing" | "done";

function invalidateData(qc: ReturnType<typeof useQueryClient>) {
  for (const k of ["drives", "charges", "samples", "imports"]) qc.invalidateQueries({ queryKey: [k] });
}

function ImportError({ err }: { err: unknown }) {
  const f = importFailure(err);
  const q = useQuery({ queryKey: ["imports"], queryFn: () => backend.listImports(), enabled: !!f.importID });
  let text = f.text;
  if (f.importID) {
    const rec = q.data?.find((r) => r.id === f.importID);
    if (q.isLoading) text = `${f.text} Checking what was saved.`;
    else if (rec?.finished_at && !rec.undone_at) text = "The import finished, but the connection dropped before it said so. See Imports below.";
    else if (rec && !rec.undone_at) text = `${f.text} ${drivesText(rec.drives)} and ${chargesText(rec.charges)} were saved so far. Undo it in Imports, then try again.`;
    else text = `${f.text} Nothing was kept. Try again.`;
  }
  return (
    <div role="alert" className="space-y-1 text-sm text-rose-300">
      <p>{text}</p>
      {f.header && f.header.length > 0 && (
        <>
          <p className="text-neutral-400">The first line of the file reads:</p>
          <code className="block break-words rounded-md bg-neutral-900 p-2 text-xs text-neutral-300">{f.header.join(", ")}</code>
          <a className="underline underline-offset-2" href={`mailto:support@rivolt.dev?subject=${encodeURIComponent("Import file header")}&body=${encodeURIComponent(f.header.join(","))}`}>
            Send us this header line
          </a>
        </>
      )}
    </div>
  );
}

// Settings → Data → Import history: pick a source, add the file(s), check the preview (nothing is written yet), then confirm. The
// browser keeps the files and sends them twice; the server stages nothing.
export function HistoryImportCard() {
  const qc = useQueryClient();
  const [step, setStep] = useState<Step>("source");
  const [sourceID, setSourceID] = useState(SOURCES[0].id);
  const [files, setFiles] = useState<File[]>([]);
  const [tz, setTZ] = useState(browserTZ());
  const [unit, setUnit] = useState("");
  const [energyUnit, setEnergyUnit] = useState("");
  const [fileVehicle, setFileVehicle] = useState("");
  const [confirmUndo, setConfirmUndo] = useState(false);
  const [vehicleID, setVehicleID] = useState("");
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const importsQ = useQuery({ queryKey: ["imports"], queryFn: () => backend.listImports(), enabled: !!preview?.already_imported });
  const [event, setEvent] = useState<ImportEvent | null>(null);
  const [result, setResult] = useState<{ drives: number; charges: number } | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const source = SOURCES.find((s) => s.id === sourceID) ?? SOURCES[0];

  const vehiclesQ = useQuery({ queryKey: ["vehicles", "owned"], queryFn: () => backend.listOwnedVehicles() });
  const vehicles = useMemo(() => vehiclesQ.data?.vehicles ?? [], [vehiclesQ.data]);
  useEffect(() => {
    if (!vehicleID && vehicles.length > 0) setVehicleID(vehicles[0].rivian_vehicle_id);
  }, [vehicles, vehicleID]);

  const picked = vehicles.find((v) => v.rivian_vehicle_id === vehicleID);
  const choices = {
    source: sourceID,
    vehicleID,
    tz,
    distanceUnit: unit || undefined,
    energyUnit: energyUnit || undefined,
    packKWh: picked?.pack_kwh && picked.pack_kwh > 0 ? picked.pack_kwh : undefined,
    vehicle: fileVehicle || undefined,
  };
  const previewM = useMutation({
    mutationFn: () => backend.importPreview(files, choices),
    onSuccess: (p) => {
      setPreview(p);
      setStep("preview");
    },
  });
  const commitM = useMutation({
    mutationFn: () => backend.importCommit(files, choices, preview!.sha256, setEvent),
    onMutate: () => setStep("importing"),
    onSuccess: (r) => {
      setResult(r);
      setStep("done");
      invalidateData(qc);
    },
    onError: () => setStep("preview"),
  });
  const undoM = useMutation({
    mutationFn: (id: string) => backend.undoImport(id),
    onSuccess: () => {
      invalidateData(qc);
      setPreview(null);
      previewM.reset();
      setConfirmUndo(false);
      setStep("files");
    },
  });

  const reset = () => {
    setStep("source");
    setFiles([]);
    setPreview(null);
    setResult(null);
    setEvent(null);
    setUnit("");
    setEnergyUnit("");
    setFileVehicle("");
    previewM.reset();
    commitM.reset();
  };
  const tooBig = files.some((f) => f.size > MAX_IMPORT_MB * 1024 * 1024);
  const need = importFailure(previewM.error).need ?? [];
  const needVehicles = importFailure(previewM.error).vehicles ?? [];
  const needsFiles = files.length === 0 || !vehicleID || tooBig || files.length > MAX_IMPORT_FILES;

  return (
    <Card title="Import history" id="import-history">
      <div data-history-import className="space-y-3 text-sm">
        {step === "source" && (
          <>
            <p className="text-neutral-300">Bring your drives and charges from another app. You see what will be added before anything is saved, and you can undo it later.</p>
            <fieldset className="space-y-2">
              <legend className="sr-only">Where is your history now?</legend>
              {SOURCES.map((s) => (
                <label key={s.id} className="flex items-center gap-2 text-neutral-200">
                  <input type="radio" name="import-source" checked={sourceID === s.id} onChange={() => setSourceID(s.id)} />
                  {s.label}
                </label>
              ))}
            </fieldset>
            <button type="button" className={btnMain} onClick={() => setStep("files")}>Continue</button>
          </>
        )}

        {step === "files" && (
          <>
            <div>
              <p className="font-medium text-neutral-200">Export from {source.label}</p>
              <ol className="mt-1 list-decimal space-y-0.5 pl-5 text-neutral-300">
                {source.how.map((h) => <li key={h}>{h}</li>)}
              </ol>
              {source.help.startsWith("/") ? (
                <Link to={source.help} className="mt-1 inline-block underline underline-offset-2">Help</Link>
              ) : (
                <a href={source.help} target="_blank" rel="noreferrer" className="mt-1 inline-block underline underline-offset-2">Help</a>
              )}
            </div>
            <div className="flex flex-col gap-1">
              <label htmlFor="import-vehicle" className="text-xs text-neutral-400">Import into vehicle</label>
              <select id="import-vehicle" value={vehicleID} onChange={(e) => setVehicleID(e.target.value)} disabled={vehicles.length === 0} className={field}>
                {vehicles.length === 0 ? (
                  <option value="">No vehicles. Link Rivian first.</option>
                ) : (
                  vehicles.map((v) => (
                    <option key={v.rivian_vehicle_id} value={v.rivian_vehicle_id}>{v.display_name || v.vin || v.rivian_vehicle_id}</option>
                  ))
                )}
              </select>
            </div>
            <div className="flex flex-col gap-1">
              <label htmlFor="import-tz" className="text-xs text-neutral-400">Time zone of the file</label>
              <input id="import-tz" value={tz} onChange={(e) => setTZ(e.target.value)} className={field} />
            </div>
            <div>
              <input
                ref={inputRef}
                type="file"
                accept=".csv,.xlsx,text/csv"
                multiple
                aria-label={`${source.label} files`}
                className="hidden"
                onChange={(e) => {
                  setFiles(Array.from(e.target.files ?? []).filter((f) => /\.(csv|xlsx)$/i.test(f.name)));
                  e.target.value = "";
                  previewM.reset();
                }}
              />
              <button type="button" className={btn} onClick={() => inputRef.current?.click()}>
                {files.length === 0 ? "Choose files" : "Choose different files"}
              </button>
              <span className="ml-2 text-xs text-neutral-500">{source.files}</span>
              {files.length > 0 && <ul className="mt-2 space-y-0.5 text-neutral-300">{files.map((f) => <li key={f.name} className="truncate">{f.name}</li>)}</ul>}
              {tooBig && <p role="alert" className="mt-1 text-rose-300">A file is over {MAX_IMPORT_MB} MB.</p>}
              {files.length > MAX_IMPORT_FILES && <p role="alert" className="mt-1 text-rose-300">Add at most {MAX_IMPORT_FILES} files.</p>}
            </div>
            {previewM.isError && <ImportError err={previewM.error} />}
            {need.includes("timezone") && <p className="text-xs text-neutral-400">Set the time zone above, then check again.</p>}
            {need.includes("distance_unit") && (
              <div className="flex flex-wrap items-center gap-2">
                <label htmlFor="import-unit" className="text-neutral-300">Distances in</label>
                <select id="import-unit" value={unit} onChange={(e) => setUnit(e.target.value)} className={field}>
                  <option value="">Choose</option>
                  <option value="km">Kilometres</option>
                  <option value="mi">Miles</option>
                </select>
              </div>
            )}
            {need.includes("energy_unit") && (
              <div className="flex flex-wrap items-center gap-2">
                <label htmlFor="import-energy" className="text-neutral-300">Energy in</label>
                <select id="import-energy" value={energyUnit} onChange={(e) => setEnergyUnit(e.target.value)} className={field}>
                  <option value="">Choose</option>
                  <option value="kwh">kWh</option>
                  <option value="wh">Wh</option>
                </select>
              </div>
            )}
            {need.includes("vehicle") && (
              <div className="flex flex-wrap items-center gap-2">
                <label htmlFor="import-file-vehicle" className="text-neutral-300">Which vehicle in the file</label>
                <select id="import-file-vehicle" value={fileVehicle} onChange={(e) => setFileVehicle(e.target.value)} className={field}>
                  <option value="">Choose</option>
                  {needVehicles.map((v) => <option key={v} value={v}>{v}</option>)}
                </select>
              </div>
            )}
            <div className="flex flex-wrap gap-2">
              <button type="button" className={btn} onClick={reset}>Back</button>
              <button type="button" className={btnMain} disabled={needsFiles || previewM.isPending} onClick={() => previewM.mutate()}>
                {previewM.isPending ? <span className="inline-flex items-center gap-2"><Spinner /> Checking</span> : "Check file"}
              </button>
            </div>
          </>
        )}

        {step === "preview" && preview && (
          <>
            {preview.already_imported && (
              <div className="rounded-md border border-amber-800 p-3 text-amber-200">
                <p>You already imported this file on {fmtDay(preview.already_imported.at)}.</p>
                {!confirmUndo ? (
                  <button type="button" className={`${btn} mt-2`} onClick={() => setConfirmUndo(true)}>
                    Undo that import
                  </button>
                ) : (
                  <div role="alertdialog" aria-label="Confirm undo" className="mt-2 rounded-md border border-rose-900 p-3 text-neutral-200">
                    <p>
                      {(() => {
                        const rec = importsQ.data?.find((r) => r.id === preview.already_imported!.id);
                        return rec ? undoWarning(rec.drives, rec.charges) : undoUnknown;
                      })()}
                    </p>
                    <div className="mt-2 flex gap-2">
                      <button type="button" className={btn} onClick={() => setConfirmUndo(false)}>Keep</button>
                      <button type="button" className="rounded-md border border-rose-800 px-3 py-1.5 text-sm text-rose-200 hover:border-rose-600 disabled:opacity-50" disabled={undoM.isPending} onClick={() => undoM.mutate(preview.already_imported!.id)}>
                        Remove them
                      </button>
                    </div>
                  </div>
                )}
              </div>
            )}
            <p className="text-neutral-200">
              {previewEmpty(preview)
                ? "Nothing new in this file."
                : `${drivesText(preview.new_drives)} and ${chargesText(preview.new_charges)} will be added, from ${fmtDay(preview.first)} to ${fmtDay(preview.last)}.`}
            </p>
            {Object.entries(preview.skipped).filter(([, n]) => n > 0).length > 0 && (
              <ul className="text-neutral-400">
                {Object.entries(preview.skipped).filter(([, n]) => n > 0).map(([reason, n]) => <li key={reason}>{n.toLocaleString()} skipped: {skipLabel(reason)}</li>)}
              </ul>
            )}
            <p className="text-xs text-neutral-500">
              Times read as {preview.assumptions.tz}
              {preview.assumptions.distance_unit ? `, distances in ${preview.assumptions.distance_unit}` : ""}
              {preview.assumptions.energy_unit ? `, energy in ${preview.assumptions.energy_unit}` : ""}.
            </p>
            {preview.warnings.map((w) => (warningText(w) ? <p key={w} className="text-amber-300">{warningText(w)}</p> : null))}
            {preview.sample.length > 0 && <SampleTable rows={preview.sample} />}
            {commitM.isError && <ImportError err={commitM.error} />}
            {undoM.isError && <ImportError err={undoM.error} />}
            <div className="flex flex-wrap gap-2">
              <button type="button" className={btn} onClick={() => setStep("files")}>Back</button>
              <button type="button" className={btnMain} disabled={previewEmpty(preview) || commitM.isPending} onClick={() => commitM.mutate()}>
                Import
              </button>
            </div>
          </>
        )}

        {step === "importing" && (
          <p role="status" className="inline-flex items-center gap-2 text-neutral-300">
            <Spinner /> Importing{event?.total ? `, ${(event.done ?? 0).toLocaleString()} of ${event.total.toLocaleString()}` : ""}
          </p>
        )}

        {step === "done" && result && (
          <>
            <p role="status" className="text-emerald-300">{addedSummary(result.drives, result.charges)}</p>
            <div className="flex gap-2">
              <Link to="/drives" className={btn}>See drives</Link>
              <button type="button" className={btn} onClick={reset}>Import another</button>
            </div>
          </>
        )}
      </div>
    </Card>
  );
}

function SampleTable({ rows }: { rows: ImportSampleRow[] }) {
  return (
    <div className="overflow-x-auto rounded-md border border-neutral-800">
      <table className="w-full text-xs">
        <thead className="bg-neutral-900 text-neutral-400">
          <tr>
            <th className="px-2 py-1 text-left font-medium">Start</th>
            <th className="px-2 py-1 text-left font-medium">Type</th>
            <th className="px-2 py-1 text-right font-medium">Miles</th>
            <th className="px-2 py-1 text-right font-medium">kWh</th>
            <th className="px-2 py-1 text-right font-medium">Battery</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-neutral-800">
          {rows.map((r, i) => (
            <tr key={i} className="text-neutral-300">
              <td className="whitespace-nowrap px-2 py-1">{fmtDay(r.started_at)}</td>
              <td className="px-2 py-1">{r.kind === "charge" ? "Charge" : "Drive"}</td>
              <td className="px-2 py-1 text-right tabular-nums">{r.distance_mi != null ? r.distance_mi.toFixed(1) : ""}</td>
              <td className="px-2 py-1 text-right tabular-nums">{r.energy_kwh != null ? r.energy_kwh.toFixed(1) : ""}</td>
              <td className="whitespace-nowrap px-2 py-1 text-right tabular-nums">{Math.round(r.start_soc_pct)}% to {Math.round(r.end_soc_pct)}%</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Settings → Data → Imports: every import with its date, source and counts, and an Undo that says what goes before it goes.
export function ImportsListCard() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["imports"], queryFn: () => backend.listImports() });
  const [asking, setAsking] = useState<string | null>(null);
  const undo = useMutation({
    mutationFn: (id: string) => backend.undoImport(id),
    onSuccess: () => {
      setAsking(null);
      invalidateData(qc);
    },
  });
  const rows: ImportRecord[] = q.data ?? [];
  return (
    <Card title="Imports" id="imports">
      <div data-imports-list className="space-y-3 text-sm">
        {q.isLoading && <Spinner />}
        {q.isError && <ErrorBox title="Couldn't load imports" detail={String(q.error)} />}
        {q.isSuccess && rows.length === 0 && <p className="text-neutral-400">Nothing imported yet.</p>}
        {rows.length > 0 && (
          <ul className="divide-y divide-neutral-800">
            {rows.map((r) => {
              const label = SOURCES.find((s) => s.id === r.source)?.label ?? r.source;
              return (
                <li key={r.id} className="space-y-2 py-2">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <p className="text-neutral-200">{label} · {fmtDay(r.started_at)}</p>
                      <p className="text-xs text-neutral-400">{drivesText(r.drives)}, {chargesText(r.charges)}</p>
                    </div>
                    {r.undone_at ? (
                      <span className="text-xs text-neutral-500">Undone {fmtDay(r.undone_at)}</span>
                    ) : (
                      <button type="button" className={btn} onClick={() => setAsking(r.id)} aria-label={`Undo ${label} import from ${fmtDay(r.started_at)}`}>Undo</button>
                    )}
                  </div>
                  {asking === r.id && (
                    <div role="alertdialog" aria-label="Confirm undo" className="rounded-md border border-rose-900 p-3">
                      <p className="text-neutral-200">{undoWarning(r.drives, r.charges)}</p>
                      <div className="mt-2 flex gap-2">
                        <button type="button" className={btn} onClick={() => setAsking(null)}>Keep</button>
                        <button type="button" className="rounded-md border border-rose-800 px-3 py-1.5 text-sm text-rose-200 hover:border-rose-600 disabled:opacity-50" disabled={undo.isPending} onClick={() => undo.mutate(r.id)}>
                          Remove them
                        </button>
                      </div>
                      {undo.isError && <p role="alert" className="mt-2 text-rose-300">{importFailure(undo.error).text}</p>}
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Card>
  );
}
