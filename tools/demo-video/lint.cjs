#!/usr/bin/env node
// Lints the demo-video sources: the manifest, the click-path scripts and the captions. No dependencies; run `node tools/demo-video/lint.cjs`.
// It exists because captions are public copy and the demo's data moves under them: every rule below is a mistake we made or nearly made.
const fs = require('fs');
const path = require('path');

const DIR = __dirname;
const STATUSES = ['active', 'blocked', 'retired'];
const VIEWPORTS = ['desktop', 'mobile'];
const STEP_KINDS = ['wait', 'goto', 'click', 'tap', 'scroll', 'scrollto', 'top', 'back', 'highlight', 'waitfor', 'waitscroll', 'mark'];
// Controls that write something. The recorder's GET-only guard aborts the request, but a clip that clicks one is a clip that fails or lies.
const MUTATING_CLICKS = [/add as a stop/i, /^save$/i, /save and plan/i, /delete/i, /remove/i, /sign in/i, /connect/i, /add price/i, /subscribe/i, /upgrade/i, /buy/i, /checkout/i, /^send$/i];
// Public wording rules: "free" appears only inside "free during the beta". Words that must stay out of clips until the launch copy is
// public (feature names that are held back, plan names) are NOT listed here: they come from DEMO_VIDEO_HELD_WORDS (newline-separated,
// case-insensitive patterns, a CI secret like the personal-data denylist) or DEMO_VIDEO_HELD_WORDS_FILE for local runs.
const BANNED_WORDING = [
  [/\bforever\b/i, 'never "forever"'],
  [/limited beta|invite[- ]only|early access/i, 'not "limited beta", "invite-only" or "early access"'],
  [/for life|always free/i, 'not "for life" or "always free"'],
  [/\$\s?\d/, 'no prices or dollar figures in captions (they roll with the demo data)'],
  [/no tracking/i, 'never claim "no tracking"'],
];
const UNIVERSAL = /\b(every|all of|always|never|guarantee[ds]?|exact(ly)?|perfect(ly)?)\b/i;
const PII = [[/[\w.+-]+@[\w-]+\.[\w.-]+/, 'an email address'], [/\b[A-HJ-NPR-Z0-9]{17}\b/, 'a VIN-shaped string']];

function captionLines(c) {
  if (Array.isArray(c)) return c;
  return [c.lead, ...(c.end || [])].filter(Boolean);
}

function heldWords(opts = {}) {
  if (opts.heldWords) return opts.heldWords;
  let raw = process.env.DEMO_VIDEO_HELD_WORDS || '';
  const f = process.env.DEMO_VIDEO_HELD_WORDS_FILE;
  if (!raw && f) raw = fs.readFileSync(f, 'utf8');
  return raw.split('\n').map((l) => l.trim()).filter(Boolean);
}

function lint(dir = DIR, opts = {}) {
  const held = heldWords(opts).map((w) => new RegExp(w, 'i'));
  const errors = [];
  const err = (m) => errors.push(m);
  const read = (f) => JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'));
  let manifest, captions;
  try { manifest = read('clips.json'); } catch (e) { return ['clips.json: ' + e.message]; }
  try { captions = read('captions.json'); } catch (e) { return ['captions.json: ' + e.message]; }
  const host = manifest.demo_host;
  if (host !== 'demo.rivolt.dev') err('clips.json: demo_host must be demo.rivolt.dev');

  const seen = new Set();
  const referencedCaptions = new Set();
  for (const clip of manifest.clips || []) {
    const n = clip.name;
    if (!n || seen.has(n)) err(`clips.json: missing or duplicate clip name ${n}`);
    seen.add(n);
    if (!STATUSES.includes(clip.status)) err(`${n}: status must be one of ${STATUSES.join(', ')}`);
    if (!clip.feature) err(`${n}: feature is required`);
    if (clip.status === 'blocked' && !clip.blocked_reason) err(`${n}: a blocked clip needs blocked_reason`);
    if (clip.status === 'retired' && !clip.retired_reason) err(`${n}: a retired clip needs retired_reason`);
    if (clip.status !== 'active') {
      if (clip.script) err(`${n}: only active clips have a script`);
      if (captions[n] || Object.keys(captions).some((k) => k.startsWith(n + '@'))) err(`${n}: ${clip.status} clip must not have captions in captions.json`);
      continue;
    }
    if (!clip.rerun_when) err(`${n}: rerun_when is required for an active clip`);
    for (const v of clip.viewports || []) if (!VIEWPORTS.includes(v)) err(`${n}: unknown viewport ${v}`);
    if (!(clip.viewports || []).length) err(`${n}: viewports is required`);
    let script;
    try { script = read(clip.script); } catch (e) { err(`${n}: script ${clip.script}: ${e.message}`); continue; }
    if (script.name !== n) err(`${n}: script name is ${script.name}`);
    lintScript(n, script, host, err);
    const marks = script.steps.filter((s) => s.do === 'mark').length;
    for (const v of clip.viewports) {
      const key = captions[n + '@' + v] ? n + '@' + v : n;
      referencedCaptions.add(key);
      const caps = captions[key];
      if (!caps) { err(`${n}: no captions for ${v}`); continue; }
      if (caps.length !== marks) err(`${key}: ${caps.length} captions but the script has ${marks} marks (one caption per mark, the last is the end card)`);
      lintCaptions(key, caps, clip.allow_universal || [], held, err);
      if (!fs.existsSync(path.join(dir, 'timing', `${n}-${v}.timing.json`)) && !(readOpt(dir, 'timing/durations.json') || {})[n]) {
        err(`${n}: no timing for ${v} (timing/${n}-${v}.timing.json or an entry in timing/durations.json)`);
      }
    }
  }
  for (const k of Object.keys(captions)) if (!referencedCaptions.has(k)) err(`captions.json: ${k} is not used by any active clip`);
  const recorder = fs.readFileSync(path.join(dir, 'record-clip.cjs'), 'utf8');
  if (!/q\.method\(\)!=='GET'&&q\.method\(\)!=='HEAD'/.test(recorder) || !/return r\.abort\(\)/.test(recorder)) err('record-clip.cjs: the GET-only guard is missing');
  if (!/const HOST='demo\.rivolt\.dev'/.test(recorder)) err('record-clip.cjs: HOST must be demo.rivolt.dev');
  for (const f of fs.readdirSync(dir, { recursive: true })) if (/\.(mp4|gif|webm|jpg|png)$/i.test(f) || f.split(path.sep).some((p) => p === 'out' || p.endsWith('.frames'))) err(`${f}: rendered output must not be committed (it goes to out/, which is git-ignored)`);
  return errors;
}

function readOpt(dir, f) { try { return JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8')); } catch { return null; } }

function lintScript(n, s, host, err) {
  if (!s.start || new URL(s.start).host !== host) err(`${n}: start must be on ${host}`);
  if (!Array.isArray(s.steps) || !s.steps.length) { err(`${n}: no steps`); return; }
  for (const st of s.steps) {
    if (!STEP_KINDS.includes(st.do)) err(`${n}: unknown step kind ${st.do} (only reads: no typing, no form fill)`);
    if (st.do === 'goto') {
      if (/^https?:/.test(st.url) && new URL(st.url).host !== host) err(`${n}: goto leaves the demo host: ${st.url}`);
    }
    if ((st.do === 'click' || st.do === 'tap') && st.text && MUTATING_CLICKS.some((r) => r.test(st.text))) err(`${n}: clicks "${st.text}", which writes data; the demo is read-only`);
    if ((st.do === 'click' || st.do === 'tap') && st.selector && /delete|remove|submit|save|add-stop/i.test(st.selector)) err(`${n}: clicks selector ${st.selector}, which looks like a write`);
  }
  const first = s.steps.findIndex((x) => x.do === 'mark');
  if (first < 0) err(`${n}: no mark steps`);
  const text = JSON.stringify(s);
  for (const [re, what] of PII) if (re.test(text)) err(`${n}: script contains ${what}`);
}

function lintCaptions(key, caps, allowUniversal, held, err) {
  caps.forEach((c, i) => {
    const lines = captionLines(c);
    const text = lines.join(' ');
    const stripped = text.replace(/free during the beta/gi, '');
    if (/\bfree\b/i.test(stripped)) err(`${key}[${i}]: "free" may only appear as "free during the beta"`);
    for (const [re, why] of BANNED_WORDING) if (re.test(stripped)) err(`${key}[${i}]: ${why}: "${text}"`);
    for (const re of held) if (re.test(stripped)) err(`${key}[${i}]: held back until the launch copy is cleared (DEMO_VIDEO_HELD_WORDS): "${text}"`);
    for (const [re, what] of PII) if (re.test(text)) err(`${key}[${i}]: contains ${what}`);
    if (!Array.isArray(c)) return;
    if (UNIVERSAL.test(text) && !allowUniversal.includes(i)) err(`${key}[${i}]: universal claim (every/all/always/never/exact) needs a check against the demo; list the caption index in clips.json allow_universal once someone has verified it: "${text}"`);
  });
  const end = caps[caps.length - 1];
  if (Array.isArray(end) || !end.end) { err(`${key}: the last caption must be an end card ({"end":[...]})`); return; }
  const endText = end.end.join(' ');
  if (!/not affiliated with Rivian/i.test(endText)) err(`${key}: end card must say it is not affiliated with Rivian`);
  if (!/Sample truck, generated data/i.test(endText)) err(`${key}: end card must say "Sample truck, generated data"`);
  if (!/free during the beta/i.test(endText)) err(`${key}: the public end card is the beta wording; the launch variant lives in rivolt-infra and is applied with ENDCARD_FILE`);
}

module.exports = { lint };
if (require.main === module) {
  if (!heldWords().length) console.error('demo-video lint: DEMO_VIDEO_HELD_WORDS is not set: the held-back-wording rule is skipped');
  const errors = lint();
  if (errors.length) { console.error(errors.map((e) => '  ' + e).join('\n')); console.error(`demo-video lint: ${errors.length} problem(s)`); process.exit(1); }
  console.log('demo-video lint: ok');
}
