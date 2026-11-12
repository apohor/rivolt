import { ApiError, type ImportPreview, type ImportSource } from "./api";

export type SourceInfo = { id: ImportSource; label: string; how: string[]; files: string; help: string };

// What each source needs, in plain words. The export steps are the source app's own; the help link is its page.
export const SOURCES: SourceInfo[] = [
  {
    id: "tezlab",
    label: "TezLab",
    how: ["In TezLab, open Settings and choose Export data.", "Download the drives file and the charges file.", "Add both files here."],
    files: "Drives and charges files",
    help: "https://help.tezlabapp.com",
  },
  {
    id: "abrp",
    label: "A Better Routeplanner",
    how: ["In ABRP, open your account and choose Export data.", "Download the trips file.", "Add it here."],
    files: "One file",
    help: "https://abetterrouteplanner.com/help",
  },
];

export const MAX_IMPORT_MB = 50;
export const MAX_IMPORT_FILES = 4;

export function browserTZ(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

export function fmtDay(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`;
export const drivesText = (n: number) => plural(n, "drive", "drives");
export const chargesText = (n: number) => plural(n, "charge", "charges");

export function addedSummary(drives: number, charges: number): string {
  return `Added ${drivesText(drives)}, ${chargesText(charges)}.`;
}

export function undoWarning(drives: number, charges: number): string {
  return `This removes ${drivesText(drives)} and ${chargesText(charges)}. Notes or tags you added to them go too.`;
}

const SKIP_REASONS: Record<string, string> = {
  overlaps_live: "already recorded by your vehicle",
  overlaps_existing: "already in Rivolt",
};
export function skipLabel(reason: string): string {
  return SKIP_REASONS[reason] ?? reason.replace(/_/g, " ");
}

const WARNINGS: Record<string, string> = {
  brings_back_deleted_history: "This brings back history you deleted earlier.",
  ambiguous_local_times: "Some times fall where the clocks change and could be read two ways.",
  positions_left_out: "Positions in this file are left out.",
};
export function warningText(code: string): string {
  return WARNINGS[code] ?? "";
}

export function previewEmpty(p: ImportPreview): boolean {
  return p.new_drives + p.new_charges === 0;
}

export type ImportFailure = {
  text: string;
  // header: the file's first line, names only (never a row value).
  header?: string[];
  // need: what the file does not say and the person must choose: distance_unit, energy_unit, timezone, vehicle.
  need?: string[];
  vehicles?: string[];
  // importID: the import a stopped commit had started, to look up in Imports.
  importID?: string;
};

// importFailure turns a preview or commit failure into one plain sentence, keyed on the server's code.
export function importFailure(err: unknown): ImportFailure {
  if (!(err instanceof ApiError)) return { text: "Something went wrong. Try again." };
  const b = (err.body && typeof err.body === "object" ? err.body : {}) as { id?: string; code?: string; header?: string[]; need?: string[]; vehicles?: string[] };
  switch (b.code) {
    case "need_choice":
      return { text: "The file does not say everything we need. Choose below.", need: b.need ?? [], vehicles: b.vehicles };
    case "unknown_columns":
    case "bad_file":
      return { text: "We don't recognize this file's columns.", header: b.header };
    case "too_many_bad_rows":
      return { text: "Too many rows in this file could not be read, so nothing was imported." };
    case "too_big":
      return { text: `That file is too big. Export a shorter range.` };
    case "preview_stale":
      return { text: "The file changed since the preview. Start again." };
    case "already_imported":
      return { text: "These files were already imported. Undo that import first to import them again." };
    case "rate_limited":
      return { text: "Too many imports just now. Try again in a few minutes." };
    case "in_progress":
      return { text: `Another import of yours is running. Wait for it, then press Import again${err.retryAfter ? ` (about ${err.retryAfter} seconds)` : ""}.` };
    case "busy":
      return { text: err.retryAfter ? `Busy right now. Try again in ${err.retryAfter} seconds.` : "Busy right now. Try again soon." };
    case "stopped":
      return b.id
        ? { text: "The import stopped part way.", importID: b.id }
        : { text: "We lost the connection, so we cannot tell whether the import went through. Look in Imports below before trying again." };
    case "vehicle_not_found":
      return { text: "That vehicle was not found. Pick it again." };
  }
  if (err.status === 404) return { text: "Importing history is not available here." };
  return { text: "Something went wrong. Try again." };
}

// undoUnknown: the undo confirmation when the import's counts are not at hand.
export const undoUnknown = "This removes everything that import added. Notes or tags you added to them go too.";
