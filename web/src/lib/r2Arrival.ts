// "Expecting an R2?": the month a driver expects delivery (a month, never a day), kept so one email can go in it. The server accepts
// this month through 24 months ahead; the choices here are exactly that window.

export type MonthChoice = { value: string; label: string };

export function arrivalMonthChoices(now: Date = new Date()): MonthChoice[] {
  const out: MonthChoice[] = [];
  for (let i = 0; i <= 24; i++) {
    const d = new Date(Date.UTC(now.getFullYear(), now.getMonth() + i, 15));
    const value = `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`;
    out.push({ value, label: d.toLocaleDateString("en-US", { month: "long", year: "numeric", timeZone: "UTC" }) });
  }
  return out;
}

// "2026-12" -> "December 2026"; a value that is not a month is dropped, never shown as "Invalid Date".
export function arrivalMonthLabel(ym: string | null | undefined): string {
  const m = ym ? /^(\d{4})-(\d{2})$/.exec(ym) : null;
  if (!m || Number(m[2]) < 1 || Number(m[2]) > 12) return "";
  return new Date(`${ym}-15T12:00:00Z`).toLocaleDateString("en-US", { month: "long", year: "numeric", timeZone: "UTC" });
}
