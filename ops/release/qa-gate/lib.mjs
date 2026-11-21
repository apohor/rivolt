// Pure parts of the release QA gate (no browser, no network): turning captures into PASS / FAIL / NOT SEEN lines,
// summarising a diff, and rendering the contact sheet. run.mjs does the I/O.

export const PASS = "PASS";
export const FAIL = "FAIL";
export const NOT_SEEN = "NOT SEEN";

// "/text/i" -> RegExp. A bare string is a case-sensitive substring.
export function toRegExp(s) {
  const m = /^\/(.*)\/([a-z]*)$/s.exec(s);
  return m ? new RegExp(m[1], m[2]) : new RegExp(s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
}

export function ignorable(msg, patterns) {
  return patterns.some((p) => new RegExp(p, "i").test(msg));
}

const line = (cap, check, status, detail = "") => ({ id: `${cap.screen}/${cap.vp}/${cap.scheme}/${check}`, screen: cap.screen, vp: cap.vp, scheme: cap.scheme, check, status, detail });

// cap: { screen, vp, scheme, ready, state, text, console[], failed[], overflow: {scrollWidth, innerWidth}|null, shot }
export function evaluateCapture(manifest, screen, cap) {
  const out = [];
  out.push(line(cap, "loaded", cap.ready ? PASS : FAIL, cap.ready ? "" : `ready condition not met: ${screen.ready}`));
  const con = cap.console.filter((m) => !ignorable(m, manifest.consoleIgnore ?? []));
  const failed = cap.failed.filter((m) => !ignorable(m, manifest.failedIgnore ?? []));
  out.push(line(cap, "no-console-errors", con.length ? FAIL : PASS, con.slice(0, 3).join(" | ")));
  out.push(line(cap, "no-failed-requests", failed.length ? FAIL : PASS, failed.slice(0, 5).join(" | ")));
  const o = cap.overflow;
  out.push(line(cap, "no-horizontal-overflow", o && o.scrollWidth > o.innerWidth + 1 ? FAIL : PASS, o ? `scrollWidth ${o.scrollWidth} vs ${o.innerWidth}` : ""));
  const texts = (list, want) => {
    for (const s of list) {
      const hit = toRegExp(s).test(cap.text);
      out.push(line(cap, `${want ? "shows" : "never-shows"} ${s}`, hit === want ? PASS : FAIL));
    }
  };
  texts(screen.must ?? [], true);
  texts([...(manifest.mustNotEverywhere ?? []), ...(screen.mustNot ?? [])], false);
  if (screen.states && cap.state && screen.states[cap.state]) {
    const st = screen.states[cap.state];
    texts((st.must ?? []).map((s) => s), true);
    texts(st.mustNot ?? [], false);
    out.push(line(cap, `state ${cap.state}`, PASS, "seen"));
  }
  return out;
}

// Declared states no context saw become one NOT SEEN line each, so a human knows which sim run is still owed.
export function stateLines(manifest, caps) {
  const out = [];
  for (const screen of manifest.screens) {
    for (const st of Object.keys(screen.states ?? {})) {
      const seen = caps.some((c) => c.screen === screen.id && c.state === st);
      if (!seen) out.push({ id: `${screen.id}/state:${st}`, screen: screen.id, vp: "-", scheme: "-", check: `state ${st}`, status: NOT_SEEN, detail: "the sample truck was not in this state during the run" });
    }
  }
  return out;
}

export function tally(lines) {
  const t = { [PASS]: 0, [FAIL]: 0, [NOT_SEEN]: 0 };
  for (const l of lines) t[l.status]++;
  return t;
}

// Files changed -> what to re-check. docs-only and release-notes-only diffs carry the validation.
export function classifyFiles(files) {
  const area = { web: [], server: [], ios: [], infra: [], notes: [], other: [] };
  for (const f of files) {
    if (/^web\//.test(f)) area.web.push(f);
    else if (/^(internal|cmd)\/|^go\.(mod|sum)$|^Dockerfile$/.test(f)) area.server.push(f);
    else if (/^ios\//.test(f)) area.ios.push(f);
    else if (/^(deploy|\.github)\//.test(f)) area.infra.push(f);
    else if (/^(release-notes\/|docs\/|README|CONTRIBUTING)|\.md$/.test(f)) area.notes.push(f);
    else area.other.push(f);
  }
  const code = area.web.length + area.server.length + area.ios.length + area.infra.length + area.other.length;
  return { area, codeFiles: code, carries: code === 0 };
}

// log: lines "sha<TAB>subject"; names: output of git diff --name-only.
export function summariseDiff(validated, rc, log, names) {
  const prs = [];
  const commits = [];
  for (const l of log.split("\n").filter(Boolean)) {
    const [sha, ...rest] = l.split("\t");
    const subject = rest.join("\t");
    commits.push({ sha: sha.slice(0, 7), subject });
    for (const m of subject.matchAll(/#(\d+)/g)) if (!prs.includes(+m[1])) prs.push(+m[1]);
  }
  const files = names.split("\n").filter(Boolean);
  return { validated, rc, commits, prs, files, ...classifyFiles(files) };
}

// The diff between the validated SHA and the rc. git(...args) runs one git call (the runner gives it a timeout and no prompts); say(line) prints
// before each call that can wait. A call that fails or times out gives { unavailable: reason }, never a throw: the gate still gets its verdict.
// Same SHA on both sides is an empty diff with no git call at all.
export function buildDiff({ git, say, validated, rc }) {
  if (validated === rc) { say(`diff: validated and rc are the same ref (${validated}): nothing differs`); return summariseDiff(validated, rc, "", ""); }
  try {
    try { git("cat-file", "-e", `${validated}^{commit}`); git("cat-file", "-e", `${rc}^{commit}`); } catch { say("diff: a commit is not local, fetching it (30 s at most)"); git("fetch", "--quiet", "origin", validated, rc); }
    say("diff: reading the log and the file list");
    return summariseDiff(validated, rc, git("log", "--format=%H%x09%s", `${validated}..${rc}`), git("diff", "--name-only", `${validated}...${rc}`));
  } catch (e) {
    return { validated, rc, unavailable: String(e.message).split("\n")[0].slice(0, 160), commits: [], prs: [], files: [], area: {}, carries: false };
  }
}

export function diffText(d) {
  if (d.unavailable) return `diff ${d.validated}..${d.rc}: NOT AVAILABLE (${d.unavailable}); QA reads the PR list by hand`;
  const a = d.area;
  const l = [`Validated ${d.validated} -> rc ${d.rc}: ${d.commits.length} commit(s), ${d.files.length} file(s), PRs ${d.prs.map((n) => "#" + n).join(" ") || "none"}`];
  l.push(`  web ${a.web.length}, server ${a.server.length}, ios ${a.ios.length}, infra ${a.infra.length}, notes/docs ${a.notes.length}, other ${a.other.length}`);
  l.push(d.carries ? "  Only notes/docs differ: the validation on the earlier commit carries." : "  Code differs: re-check the screens these files touch (listed in the sheet).");
  for (const c of d.commits) l.push(`  ${c.sha} ${c.subject}`);
  return l.join("\n");
}

const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

export function renderSheet({ meta, lines, caps, diff, manifest }) {
  const t = tally(lines);
  const order = { [FAIL]: 0, [NOT_SEEN]: 1, [PASS]: 2 };
  const interesting = lines.filter((l) => l.status !== PASS).sort((a, b) => order[a.status] - order[b.status]);
  const rows = interesting.map((l) => `<tr class="${l.status.replace(" ", "-")}"><td>${esc(l.status)}</td><td>${esc(l.id)}</td><td>${esc(l.detail)}</td></tr>`).join("");
  const vps = manifest.viewports.map((v) => v.name);
  const grid = manifest.screens.map((s) => {
    const cells = [];
    for (const vp of vps) for (const scheme of manifest.schemes) {
      const c = caps.find((x) => x.screen === s.id && x.vp === vp && x.scheme === scheme);
      cells.push(c && c.shot ? `<figure class="v${vp}"><a href="${esc(c.shot)}"><img loading="lazy" src="${esc(c.shot)}"></a><figcaption>${esc(vp)} ${esc(scheme)}${c.state ? " &middot; " + esc(c.state) : ""}</figcaption></figure>` : `<figure><figcaption>${esc(vp)} ${esc(scheme)}: no capture</figcaption></figure>`);
    }
    return `<section><h2>${esc(s.id)}</h2><div class="g">${cells.join("")}</div></section>`;
  }).join("");
  const d = diff
    ? `<section><h2>Diff since the validated commit</h2><pre>${esc(diffText(diff))}</pre><details><summary>${diff.files.length} files</summary><pre>${esc(diff.files.join("\n"))}</pre></details></section>`
    : "";
  return `<!doctype html><meta charset="utf-8"><title>Rivolt QA gate</title>
<style>body{font:14px system-ui;margin:16px;background:#fafafa;color:#111}table{border-collapse:collapse}td{padding:2px 8px;border-bottom:1px solid #ddd}
.FAIL td:first-child{background:#fbb}.NOT-SEEN td:first-child{background:#fe9}.g{display:flex;flex-wrap:wrap;gap:8px;align-items:flex-start}figure{margin:0}
figure.v393 img{width:196px}figure.v1440 img{width:480px}img{border:1px solid #ccc;display:block}pre{background:#eee;padding:8px;overflow:auto}</style>
<h1>Rivolt QA gate</h1>
<p>${esc(meta.base)} &middot; version ${esc(meta.version ?? "?")}${meta.expectVersion ? " (expected " + esc(meta.expectVersion) + ")" : ""} &middot; ${esc(meta.at)} &middot; ${meta.seconds}s</p>${meta.invalid ? `<p><b>INVALID: ${esc(meta.invalid)}. Not a verdict; run again once preview is stable.</b></p>` : ""}
<p><b>${t[PASS]} PASS &middot; ${t[FAIL]} FAIL &middot; ${t[NOT_SEEN]} NOT SEEN</b></p>
<table>${rows || "<tr><td>Every line passed.</td></tr>"}</table>${d}${grid}`;
}
