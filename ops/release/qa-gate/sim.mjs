// The checks on the two sim bookings the runner makes (the short drive, then the short charge queued behind it).
import { FAIL, PASS } from "./lib.mjs";

// A refusal that carries free_at says when to book again: the gate can wait for it or record the sim as NOT SEEN.
export const nextBookable = (b) => (b?.free_at && !Number.isNaN(Date.parse(b.free_at)) ? `; next bookable at ${new Date(b.free_at).toISOString().slice(11, 16)}Z` : "");
// A refusal because a run is under way carries that run (read from GET /api/admin/sim), so the report says whose slot it is and when it ends.
export const underWay = (b) => (b?.under_way ? `; under way: ${b.under_way.kind} ${b.under_way.start} to ${b.under_way.end}` : "");
const line = (id, ok, detail) => ({ id: `sim:${id}`, screen: "sim", vp: "-", scheme: "-", check: id, status: ok ? PASS : FAIL, detail });
const run = (r) => r?.body?.active ?? {};

export function pairLines(first, second) {
  const d = run(first), c = run(second);
  return [
    line("drive booked", first.status === 200, first.status === 200 ? `${d.id} ${d.start} to ${d.end}` : `${first.status} ${first.body?.error ?? ""}${nextBookable(first.body)}${underWay(first.body)}`),
    line("drive kind", /_short$/.test(d.kind ?? ""), `kind ${d.kind ?? "none"}: the short variant, not the 28-minute run`),
    line("charge booked", second.status === 200, second.status === 200 ? `${c.id} ${c.start} to ${c.end}` : `${second.status} ${second.body?.error ?? ""}${nextBookable(second.body)}${underWay(second.body)}`),
    line("charge kind", /_short$/.test(c.kind ?? ""), `kind ${c.kind ?? "none"}`),
    line("charge starts when the drive ends", !!d.end && c.start === d.end, `drive end ${d.end}, charge start ${c.start}`),
    line("pair within 11 minutes", !!d.start && !!c.end && new Date(c.end) - new Date(d.start) <= 11 * 60000, `${d.start} to ${c.end}`),
  ];
}

// A Set-Cookie header line as a cookie for ctx.addCookies (a fulfilled response does not set cookies in WebKit, so the route does it).
export function cookieFrom(line, url) {
  const [pair, ...attrs] = line.split(";").map((x) => x.trim());
  const i = pair.indexOf("=");
  const c = { name: pair.slice(0, i), value: pair.slice(i + 1), url };
  for (const at of attrs) {
    const [k, v = ""] = at.split("="); const key = k.toLowerCase();
    if (key === "path") { c.path = v; delete c.url; c.domain = new URL(url).hostname; }
    else if (key === "httponly") c.httpOnly = true;
    else if (key === "secure") c.secure = true;
    else if (key === "samesite") c.sameSite = { strict: "Strict", lax: "Lax", none: "None" }[v.toLowerCase()];
    else if (key === "max-age") c.expires = Math.floor(Date.now() / 1000) + Number(v);
  }
  if (c.path && !c.domain) c.domain = new URL(url).hostname;
  if (!c.path) { c.path = "/"; c.domain = new URL(url).hostname; delete c.url; }
  return c;
}

// The Cloudflare Access service token goes only to requests to the admin origin, and never as the context's extraHTTPHeaders (those go to every
// host the page touches). A request to the admin origin is fetched here with redirects off (continue() would apply the headers to every redirect
// leg, off-origin ones too). A redirect answer to a page navigation becomes a page that moves on, so the next navigation is routed again; a
// redirect answer to a sub-request is aborted.
export function accessRoute(adminOrigin, token, addCookies = async () => {}) {
  // An error from route.fetch carries the request headers, token included, in its message: it is never passed on.
  return async (route) => {
    try { await handle(route); } catch { await route.abort().catch(() => {}); }
  };
  async function handle(route) {
    const req = route.request();
    const ours = new URL(req.url()).origin === adminOrigin;
    const nav = req.isNavigationRequest();
    // Another origin's sub-requests go as the browser made them. Its navigations are followed by hand too, so that the redirect that comes back to
    // the admin origin is routed (and gets the token) instead of being followed by the browser without it.
    if (!ours && !nav) return route.continue({ headers: req.headers() });
    const res = await route.fetch({ headers: ours ? { ...req.headers(), ...token } : req.headers(), maxRedirects: 0 });
    const jar = (res.headersArray?.() ?? []).filter((h) => h.name.toLowerCase() === "set-cookie").map((h) => cookieFrom(h.value, req.url()));
    if (jar.length) await addCookies(jar);
    if (res.status() < 300 || res.status() > 399) return route.fulfill({ response: res });
    if (!nav) return route.abort();
    const to = new URL(res.headers().location, req.url()).href;
    const h = { ...res.headers(), "content-type": "text/html" };
    delete h.location; delete h["content-length"]; delete h["content-encoding"]; delete h["set-cookie"];
    return route.fulfill({ status: 200, headers: h, body: `<meta http-equiv="refresh" content="0;url=${to}">` });
  }
}

// Signs in to the admin site in a new context (the token only through accessRoute), books the pair with per-call headers on the admin origin
// only, signs out of the admin session and closes the context. login(page, root) is the runner's sign-in form.
export async function bookPair({ browser, adminBase, token, login }) {
  const ctx = await browser.newContext();
  try {
    await ctx.route("**/*", accessRoute(new URL(adminBase).origin, token, (c) => ctx.addCookies(c)));
    await login(await ctx.newPage(), adminBase);
    const post = async (path) => {
      const r = await ctx.request.post(`${adminBase}${path}`, { data: {}, headers: token, maxRedirects: 0 });
      const out = { status: r.status(), body: await r.json().catch(() => ({})) };
      if (out.status === 409 && /under way/.test(out.body?.error ?? "")) {
        const g = await ctx.request.get(`${adminBase}/api/admin/sim`, { headers: token, maxRedirects: 0 }).catch(() => null);
        const a = g?.ok() ? (await g.json().catch(() => ({})))?.active : null;
        if (a) out.body = { ...out.body, under_way: { kind: a.kind, start: a.start, end: a.end } };
      }
      return out;
    };
    const first = await post("/api/admin/sim/drive?short=1");
    const second = first.status === 200 ? await post("/api/admin/sim/charge?short=1&queue=1") : { status: 0, body: {} };
    return { first, second };
  } finally {
    await ctx.unrouteAll({ behavior: "ignoreErrors" }).catch(() => {});
    await ctx.request.post(`${adminBase}/api/auth/logout`, { data: {}, headers: token, maxRedirects: 0 }).catch(() => {});
    await ctx.close();
  }
}
