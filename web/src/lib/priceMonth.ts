// "2026-10" -> "Oct 2026"; empty when the month isn't there.
export function priceMonth(ym: string | undefined): string {
  const m = ym ? /^(\d{4})-(\d{2})$/.exec(ym) : null;
  // A real month only: "2026-13" is malformed, never "Invalid Date" on the page.
  if (!m || Number(m[2]) < 1 || Number(m[2]) > 12) return "";
  const d = new Date(`${ym}-15T12:00:00Z`);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleDateString("en-US", { month: "short", year: "numeric", timeZone: "UTC" });
}
