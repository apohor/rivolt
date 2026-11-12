import { ApiError, type ImportPreview, type ImportSource } from "./api";

export type SourceInfo = { id: ImportSource; label: string; how: string[]; files: string; multi: boolean; help: string };

// What each source needs, in plain words. The export steps are the source app's own; the help link is its page.
export const SOURCES: SourceInfo[] = [
  {
    id: "tezlab",
    label: "TezLab",
    how: ["In TezLab, open Settings and choose Export data.", "Download the drives file and the charges file.", "Add both files here."],
    files: "Drives and charges CSV files",
    multi: true,
    help: "https://help.tezlabapp.com",
  },
  {
    id: "abrp",
    label: "A Better Routeplanner",
    how: ["In ABRP, open your account and choose Export data.", "Download the trips file.", "Add it here."],
    files: "One CSV file",
    multi: false,
    help: "https://abetterrouteplanner.com/help",
  },
  {
    id: "electrafi",
    label: "ElectraFi",
    how: ["In ElectraFi, export your history as CSV.", "Add every file you get here."],
    files: "One or more CSV files",
    multi: true,
    help: "/moving-from-electrafi",
  },
];

export const MAX_IMPORT_MB = 50;

export function browserTZ(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

export function fmtDay(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

export function fmtMonth(ym: string): string {
  const d = new Date(`${ym}-01T12:00:00Z`);
  if (Number.isNaN(d.getTime())) return ym;
  return d.toLocaleDateString(undefined, { year: "numeric", month: "long", timeZone: "UTC" });
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
  duplicate: "already in Rivolt",
  covered: "already recorded by your vehicle",
  too_short: "too short to count",
  invalid: "could not be read",
};
export function skipLabel(reason: string): string {
  return SKIP_REASONS[reason] ?? reason.replace(/_/g, " ");
}

export function previewEmpty(p: ImportPreview): boolean {
  return p.new.drives + p.new.charges === 0;
}

// importErrorText turns a preview or commit failure into one plain sentence. Header names are the only file content ever shown.
export function importErrorText(err: unknown): { text: string; headers?: string[] } {
  if (!(err instanceof ApiError)) return { text: "Something went wrong. Try again." };
  const b = (err.body && typeof err.body === "object" ? err.body : {}) as { error?: string; headers?: string[] };
  switch (err.status) {
    case 404:
      return { text: "Importing history is not available here." };
    case 409:
      return b.error === "hash_changed"
        ? { text: "The file changed since the preview. Start again." }
        : { text: "This import is already done." };
    case 413:
      return { text: `That file is over ${MAX_IMPORT_MB} MB. Split it and try again.` };
    case 422:
      if (b.error === "unrecognized_headers") return { text: "We don't recognize this file's columns.", headers: b.headers };
      return { text: "Too many rows in this file could not be read, so nothing was imported." };
    case 429:
      return { text: "Too many imports just now. Try again in a few minutes." };
    case 503:
      return { text: err.retryAfter ? `Busy right now. Try again in ${err.retryAfter} seconds.` : "Busy right now. Try again soon." };
    default:
      return { text: "Something went wrong. Try again." };
  }
}
