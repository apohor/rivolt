// The checks on the two sim bookings the runner makes (the short drive, then the short charge queued behind it).
import { FAIL, PASS } from "./lib.mjs";

const line = (id, ok, detail) => ({ id: `sim:${id}`, screen: "sim", vp: "-", scheme: "-", check: id, status: ok ? PASS : FAIL, detail });
const run = (r) => r?.body?.active ?? {};

export function pairLines(first, second) {
  const d = run(first), c = run(second);
  return [
    line("drive booked", first.status === 200, first.status === 200 ? `${d.id} ${d.start} to ${d.end}` : `${first.status} ${first.body?.error ?? ""}`),
    line("drive kind", /_short$/.test(d.kind ?? ""), `kind ${d.kind ?? "none"}: the short variant, not the 28-minute run`),
    line("charge booked", second.status === 200, second.status === 200 ? `${c.id} ${c.start} to ${c.end}` : `${second.status} ${second.body?.error ?? ""}`),
    line("charge kind", /_short$/.test(c.kind ?? ""), `kind ${c.kind ?? "none"}`),
    line("charge starts when the drive ends", !!d.end && c.start === d.end, `drive end ${d.end}, charge start ${c.start}`),
    line("pair within 11 minutes", !!d.start && !!c.end && new Date(c.end) - new Date(d.start) <= 11 * 60000, `${d.start} to ${c.end}`),
  ];
}
