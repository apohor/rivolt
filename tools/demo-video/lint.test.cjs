// node --test tools/demo-video/lint.test.cjs: the lint must catch each mistake it was written for, and pass the real sources.
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { lint } = require('./lint.cjs');

// Placeholders: the real list is private (a CI secret), so the tests use made-up words.
const HELD = ['moonbeam', 'sparkle[- ]mode'];
function sandbox(mutate, opts = { heldWords: HELD }) {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), 'demo-video-'));
  fs.cpSync(__dirname, d, { recursive: true, filter: (s) => !/lint(\.test)?\.cjs$/.test(s) });
  const rw = (f, fn) => { const p = path.join(d, f); const v = JSON.parse(fs.readFileSync(p, 'utf8')); fn(v); fs.writeFileSync(p, JSON.stringify(v)); };
  mutate({ dir: d, rw });
  try { return lint(d, opts); } finally { fs.rmSync(d, { recursive: true, force: true }); }
}
const has = (errors, re) => assert.ok(errors.some((e) => re.test(e)), `expected an error matching ${re}, got:\n${errors.join('\n')}`);

test('the real sources are clean', () => assert.deepStrictEqual(lint(__dirname, { heldWords: HELD }), []));

test('a universal claim without a check fails (the "every charge" caption over Price unknown rows)', () => {
  has(sandbox(({ rw }) => rw('captions.json', (c) => { c['clip-cost'][3] = ['Every charge shows where its price came from.']; })), /universal claim/);
});
test('"free" outside "free during the beta" fails', () => {
  has(sandbox(({ rw }) => rw('captions.json', (c) => { c['clip-share'][1] = ['Sharing is free.']; })), /"free" may only appear/);
});
test('"forever" and prices fail', () => {
  const e = sandbox(({ rw }) => rw('captions.json', (c) => { c['clip-share'][0] = ['Free forever for $3.99']; }));
  has(e, /forever/); has(e, /dollar figures/);
});
test('held-back wording fails (list supplied by the private DEMO_VIDEO_HELD_WORDS)', () => {
  has(sandbox(({ rw }) => rw('captions.json', (c) => { c['clip-trip-1day'][2] = ['The moonbeam view shows more.']; c['clip-trip-1day@mobile'][2] = ['Try sparkle mode.']; })), /held back/);
});
test('an end card without the non-affiliation line fails', () => {
  has(sandbox(({ rw }) => rw('captions.json', (c) => { c['clip-share'][4].end = ['rivolt.dev · free during the beta', 'Sample truck, generated data.']; })), /not affiliated/);
});
test('the caption count must match the marks', () => {
  has(sandbox(({ rw }) => rw('captions.json', (c) => { c['clip-share'].pop(); })), /captions but the script has/);
});
test('a click on a control that writes fails', () => {
  has(sandbox(({ rw }) => rw('scripts/clip-share.json', (s) => { s.steps.splice(1, 0, { do: 'click', text: 'Add as a stop', s: 1 }); })), /writes data/);
});
test('leaving the demo host fails', () => {
  has(sandbox(({ rw }) => rw('scripts/clip-share.json', (s) => { s.steps.splice(1, 0, { do: 'goto', url: 'https://example.com/' }); })), /leaves the demo host/);
});
test('typing is not a step', () => {
  has(sandbox(({ rw }) => rw('scripts/clip-share.json', (s) => { s.steps.splice(1, 0, { do: 'type', text: 'x' }); })), /unknown step kind type/);
});
test('a blocked clip needs a reason; a blocked clip has no captions', () => {
  const e = sandbox(({ rw }) => rw('clips.json', (m) => { delete m.clips.find((c) => c.name === 'clip-planner').blocked_reason; }));
  has(e, /needs blocked_reason/);
  has(sandbox(({ rw }) => rw('captions.json', (c) => { c['clip-planner'] = [['x']]; })), /must not have captions/);
});
test('rendered output must not be committed', () => {
  has(sandbox(({ dir }) => fs.writeFileSync(path.join(dir, 'clip.mp4'), 'x')), /rendered output/);
});
test('the recorder keeps its GET-only guard', () => {
  has(sandbox(({ dir }) => {
    const p = path.join(dir, 'record-clip.cjs');
    fs.writeFileSync(p, fs.readFileSync(p, 'utf8').replace("q.method()!=='GET'", "false"));
  }), /GET-only guard/);
});
