# Demo videos

A repeatable way to make the short demo clips (and re-make them when a feature ships). Every clip is recorded from **demo.rivolt.dev, signed out, read-only** (generated sample data; a guard aborts any request that is not a GET and any request to another host). Nothing here touches a real account, and nothing is uploaded or posted: DA clears each rendered file by hash and the owner posts.

## Run it

```
cd web && npm ci            # once, for Playwright
make demo-videos            # every active clip, desktop + mobile (about 15 minutes: the demo rate-limits heavy reads, so recordings are 70 s apart)
make demo-videos CLIP=clip-share
make demo-video-lint        # the checks only, no recording (this is what CI runs)
```

Needs `ffmpeg`. Output goes to `tools/demo-video/out/` (git-ignored): per clip, a captioned mp4 and gif per viewport (1920x1080 and 1080x1920, 60 fps, frames captured at 2x/3x device scale), the uncaptioned mp4, a `contact-*.png` frame sheet for review, and `out/manifest.json` with the sha256 of every file, the demo-data date and the tools commit.

## What is where

| File | Role |
|---|---|
| `clips.json` | The manifest: one entry per clip, its status (active, blocked, retired), the feature it shows and `rerun_when`. A blocked clip says what unblocks it. |
| `scripts/<clip>.json` | The click path: steps (`goto`, `click`, `scroll`, `highlight`, `wait`, `waitfor`, `mark`, ...). A `mark` is placed after the screen state is reached; one caption starts at each mark. |
| `captions.json` | The caption text per clip (`<clip>@mobile` overrides the base entry) and the end card, the last entry. One caption per mark. |
| `timing/` | How long each caption stays up (from the cleared clips). |
| `record-clip.cjs`, `build-clip.sh`, `make-caption-pngs.cjs`, `make-demo-videos` | Record (Playwright screencast), render captions, encode (ffmpeg), assemble. |
| `lint.cjs`, `lint.test.cjs` | The checks below. |

## Updating for a new feature

1. Add the clip to `clips.json` (or flip a blocked one to `active`), write its `scripts/<clip>.json` and its captions. Open the demo and check the clip shows what the captions say.
2. `make demo-video-lint`, then `make demo-videos CLIP=<clip>`.
3. Look at the contact sheet and the frames. Then send the files to DA with their hashes from `manifest.json`.
4. After a release, re-run any clip whose `rerun_when` names a screen the release changed. The demo's data rolls with the calendar (month totals, dates), so re-record active clips about monthly rather than reuse an old file.

## What the lint checks

- Wording: "free" appears only as "free during the beta" (never "forever", "limited beta", "invite-only", "early access", "always free"), no prices, no "no tracking". Words that must stay out of clips until the launch copy is public come from a private list (`DEMO_VIDEO_HELD_WORDS`, a CI secret; `DEMO_VIDEO_HELD_WORDS_FILE` for local runs); the lint says when it is not set.
- Universal claims (every, all, always, never, exact) fail unless someone checked them against the demo and listed the caption index in `allow_universal`. The cost clip once said "every charge shows where its price came from" over rows that read "Price unknown".
- Scripts: only read steps (no typing), start on demo.rivolt.dev, never leave it, never click a control that writes (Add as a stop, Save, Delete, Sign in, Add price, Subscribe, ...), no emails or VIN-shaped strings; one caption per mark.
- Every end card says "Unofficial, not affiliated with Rivian" and "Sample truck, generated data".
- The recorder still has its GET-only guard and host lock; no rendered output (mp4, gif, frames) is committed.

Not automated yet: asserting that what a caption says is on screen at its mark (for example that no "Price unknown" row is visible under a "every price" caption). That needs per-caption selectors in the scripts and is the next step.

## The end card

The public end card says "free during the beta". The launch wording is not public yet and lives in the private rivolt-infra repo; apply it with `ENDCARD_FILE=<json with {"end": [line, ...]}> make demo-videos` once DA has cleared it, and the lint's beta-wording rule will not run against that file.

## Rules

Demo data only; GET-only; nothing is auto-posted; any change to caption wording or an end card goes to DA first; no real user or owner data, no prod figures, no prices, and nothing the launch copy has not published yet.
