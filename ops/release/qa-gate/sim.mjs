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

// The Cloudflare Access service token goes only to the admin origin. Not as the context's extraHTTPHeaders (those go to every host the page
// touches: the identity server, fonts, CDNs). Requests to any other origin are continued as the browser made them. A redirect leg is not routed
// again, so a navigation that answers 3xx is followed by hand: the answer becomes a page that moves on, and that next navigation is routed.
export function accessRoute(adminOrigin, token) {
  return async (route) => {
    const req = route.request();
    const ours = new URL(req.url()).origin === adminOrigin;
    const headers = ours ? { ...req.headers(), ...token } : req.headers();
    if (!req.isNavigationRequest()) return route.continue({ headers });
    const res = await route.fetch({ headers, maxRedirects: 0 });
    if (res.status() < 300 || res.status() > 399) return route.fulfill({ response: res });
    const to = new URL(res.headers().location, req.url()).href;
    const h = { ...res.headers(), "content-type": "text/html" };
    delete h.location; delete h["content-length"];
    return route.fulfill({ status: 200, headers: h, body: `<meta http-equiv="refresh" content="0;url=${to}">` });
  };
}

// Signs in to the admin site in a new context (the token only through accessRoute), books the pair with per-call headers on the admin origin
// only, signs out of the admin session and closes the context. login(page, root) is the runner's sign-in form.
export async function bookPair({ browser, adminBase, token, login }) {
  const ctx = await browser.newContext();
  try {
    await ctx.route("**/*", accessRoute(new URL(adminBase).origin, token));
    await login(await ctx.newPage(), adminBase);
    const post = async (path) => { const r = await ctx.request.post(`${adminBase}${path}`, { data: {}, headers: token }); return { status: r.status(), body: await r.json().catch(() => ({})) }; };
    const first = await post("/api/admin/sim/drive?short=1");
    const second = first.status === 200 ? await post("/api/admin/sim/charge?short=1&queue=1") : { status: 0, body: {} };
    await ctx.request.post(`${adminBase}/api/auth/logout`, { data: {}, headers: token }).catch(() => {});
    return { first, second };
  } finally {
    await ctx.close();
  }
}
