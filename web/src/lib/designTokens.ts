// Design tokens for the iPhone app, taken from the web's new look.
// buildTokens() resolves every token from the web's own CSS, the way the browser
// would, and the result is committed as ios/design/tokens.json; designTokens.test.ts
// rebuilds it and fails when the two differ, so the app can't drift silently.
//
// Colours are kept as the CSS's OKLCH plus an sRGB hex (clamped to the sRGB gamut)
// and alpha for Swift. Two kinds of token:
//  - roles: a CSS variable (--color-neutral-950, ...), resolved per theme through
//    the same cascade the page uses (the new look's block, then the theme block,
//    then :root, then Tailwind's defaults);
//  - literals: a fragment written straight into a rule or component (a glow, a
//    gauge gradient stop, a type size), checked to still appear in its source.

export type Theme = "dark" | "light";
export type Sources = Record<string, string>; // path (from web/) -> file text

// ---- CSS variables ----------------------------------------------------------

/** Custom properties per selector, for every rule in the file (nested in @media too). */
export function cssVars(css: string): Map<string, Record<string, string>> {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const out = new Map<string, Record<string, string>>();
  const stack: string[] = [];
  let buf = "";
  for (const ch of text) {
    if (ch === "{") {
      stack.push(buf.trim());
      buf = "";
    } else if (ch === "}") {
      flush(stack[stack.length - 1], buf);
      stack.pop();
      buf = "";
    } else if (ch === ";") {
      flush(stack[stack.length - 1], buf);
      buf = "";
    } else {
      buf += ch;
    }
  }
  return out;

  function flush(selector: string | undefined, decl: string) {
    const m = /^\s*(--[a-z0-9-]+)\s*:\s*([\s\S]+?)\s*$/.exec(decl);
    if (!selector || !m) return;
    const sel = selector.replace(/\s+/g, " ");
    const vars = out.get(sel) ?? {};
    vars[m[1]] = m[2];
    out.set(sel, vars);
  }
}

// The cascade the page applies, most specific first.
const CASCADE: Record<Theme, { file: string; selector: string }[]> = {
  dark: [
    { file: "src/index.css", selector: ":root:not([data-theme='light'])" },
    { file: "src/index.css", selector: "html:not([data-theme='light'])" },
    { file: "src/index.css", selector: ":root" },
    { file: "node_modules/tailwindcss/theme.css", selector: "@theme default" },
  ],
  light: [
    { file: "src/index.css", selector: ":root[data-theme='light']" },
    { file: "src/index.css", selector: "html[data-theme='light']" },
    { file: "src/index.css", selector: ":root" },
    { file: "node_modules/tailwindcss/theme.css", selector: "@theme default" },
  ],
};

export function resolveVar(src: Sources, name: string, theme: Theme): { value: string; from: string } {
  for (const step of CASCADE[theme]) {
    const vars = cssVars(src[step.file] ?? "").get(step.selector);
    if (vars && name in vars) return { value: vars[name], from: `${step.file} ${step.selector}` };
  }
  throw new Error(`${name} is not defined for the ${theme} theme`);
}

// ---- OKLCH -> sRGB ------------------------------------------------------------

export type Colour = { oklch: string; hex: string; alpha: number };

/** Parses oklch(L% C H [/ A]) ("none" counts as 0) into a hex and alpha. */
export function colour(oklch: string): Colour {
  const m = /^oklch\(\s*([\d.]+)%\s+([\d.]+|none)\s+([\d.]+|none)\s*(?:\/\s*([\d.]+))?\s*\)$/.exec(oklch.trim());
  if (!m) throw new Error(`not an oklch() colour: ${oklch}`);
  const num = (s: string | undefined, d: number) => (s === undefined ? d : s === "none" ? 0 : Number(s));
  const L = num(m[1], 0) / 100;
  const C = num(m[2], 0);
  const h = (num(m[3], 0) * Math.PI) / 180;
  const a = C * Math.cos(h);
  const b = C * Math.sin(h);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const mm = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const lin = [
    4.0767416621 * l - 3.3077115913 * mm + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * mm - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * mm + 1.707614701 * s,
  ];
  const enc = (x: number) => {
    const c = Math.min(1, Math.max(0, x));
    const v = c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055;
    return Math.round(v * 255)
      .toString(16)
      .padStart(2, "0")
      .toUpperCase();
  };
  return { oklch: oklch.trim(), hex: `#${lin.map(enc).join("")}`, alpha: num(m[4], 1) };
}

// ---- What the app uses ------------------------------------------------------

// Semantic roles -> the web variable each one reads. Only what the iPhone screens show.
export const ROLES: Record<string, { cssVar: string; use: string }> = {
  background: { cssVar: "--color-neutral-950", use: "page background (bg-neutral-950)" },
  card: { cssVar: "--color-neutral-900", use: "card surface (bg-neutral-900, at 50% on cards, 60% on the Now hero)" },
  border: { cssVar: "--color-neutral-800", use: "card and bar borders, chart gridlines (gridlines at 55% opacity)" },
  borderStrong: { cssVar: "--color-neutral-700", use: "neutral pill border" },
  textPrimary: { cssVar: "--color-neutral-100", use: "values, the gauge's limit tick" },
  textBody: { cssVar: "--color-neutral-300", use: "Now headline, body text; the gauge track (at 14%)" },
  textSecondary: { cssVar: "--color-neutral-400", use: "secondary text, '%' after the hero number, neutral pill text" },
  textTertiary: { cssVar: "--color-neutral-500", use: "small-caps labels (.ui-label), inactive tab" },
  accent: { cssVar: "--color-emerald-300", use: "Live pill text, active tab, positive figures" },
  accentFill: { cssVar: "--color-emerald-500", use: "bar fills, dots; Live pill border at 30%" },
  accentButton: { cssVar: "--color-emerald-600", use: "solid buttons (white text)" },
  estimate: { cssVar: "--color-amber-300", use: "estimates and 'Last seen' (warn) pill text" },
  estimateFill: { cssVar: "--color-amber-500", use: "warn pill border at 40%, fast-charge node" },
  critical: { cssVar: "--color-rose-300", use: "low battery figures" },
  criticalBorder: { cssVar: "--color-rose-500", use: "Now hero border when critical, at 50%" },
  drive: { cssVar: "--color-sky-300", use: "drive figures and labels" },
  // Charging places, as the Money split and receipts and the Activity rail draw them: the dot / icon, then the bar fill.
  home: { cssVar: "--color-emerald-400", use: "Home dots (Money split legend, receipt rows)" },
  away: { cssVar: "--color-sky-400", use: "Away dots (Money split legend, receipt rows)" },
  awayFill: { cssVar: "--color-sky-500", use: "Away bar segment (Money split); Away chip border at 40%" },
  fast: { cssVar: "--color-amber-400", use: "fast-charge dots and the ⚡ (Money split legend, receipt rows, Activity rail)" },
  fastFill: { cssVar: "--color-amber-500", use: "Fast bar segment (Money split); Fast chip border at 40%" },
  edge: { cssVar: "--ui-edge", use: "1px top-edge highlight inside cards" },
  brandMark: { cssVar: "--color-emerald-400", use: "the Rivolt logo (Logo.tsx, text-emerald-400 in the header)" },
};

type Literal = { file: string; css: string; use: string };

// Values written straight into a rule or a component, per theme where they differ.
export const LITERALS: Record<string, Partial<Record<Theme | "both", Literal>>> = {
  bar: {
    dark: { file: "src/index.css", css: "background-color: oklch(13.5% 0.009 262)", use: "top bar and bottom tab bar, fully solid" },
    light: { file: "src/index.css", css: "background-color: oklch(99% 0 0)", use: "top bar and bottom tab bar, fully solid" },
  },
  // The Now hero's light pool at rest and while charging; heroTintDriving while driving.
  heroTint: {
    dark: { file: "src/index.css", css: "radial-gradient(70% 90% at 14% 0%, oklch(78% 0.15 168 / 0.13), transparent 72%)", use: "light pool behind the Now hero metric" },
    light: { file: "src/index.css", css: "radial-gradient(70% 90% at 14% 0%, oklch(80% 0.12 168 / 0.2), transparent 72%)", use: "light pool behind the Now hero metric" },
  },
  pageGlow: {
    dark: { file: "src/index.css", css: "radial-gradient(120% 60% at 50% -10%, oklch(40% 0.05 250 / 0.18), transparent 70%)", use: "glow at the top of the page (body background, fixed)" },
    light: { file: "src/index.css", css: "radial-gradient(120% 60% at 50% -10%, oklch(90% 0.05 168 / 0.35), transparent 70%)", use: "glow at the top of the page (body background, fixed)" },
  },
  heroTintDriving: {
    dark: { file: "src/index.css", css: "radial-gradient(70% 90% at 14% 0%, oklch(76% 0.12 235 / 0.13), transparent 72%)", use: "light pool behind the Now hero metric while driving (replaces heroTint)" },
    light: { file: "src/index.css", css: "radial-gradient(70% 90% at 14% 0%, oklch(80% 0.1 235 / 0.2), transparent 72%)", use: "light pool behind the Now hero metric while driving (replaces heroTint)" },
  },
  cardSheen: {
    dark: { file: "src/index.css", css: "linear-gradient(180deg, oklch(100% 0 0 / 0.03), transparent 45%)", use: "faint top-down sheen on cards" },
    light: { file: "src/index.css", css: "linear-gradient(180deg, oklch(100% 0 0 / 0.7), transparent 55%)", use: "faint top-down sheen on cards" },
  },
  cardShadowNear: {
    dark: { file: "src/index.css", css: "0 1px 2px oklch(0% 0 0 / 0.45)", use: "card shadow, near layer" },
    light: { file: "src/index.css", css: "0 1px 2px oklch(30% 0.02 262 / 0.07)", use: "card shadow, near layer" },
  },
  cardShadowFar: {
    dark: { file: "src/index.css", css: "0 8px 24px -12px oklch(0% 0 0 / 0.5)", use: "card shadow, far layer" },
    light: { file: "src/index.css", css: "0 10px 28px -16px oklch(30% 0.03 262 / 0.22)", use: "card shadow, far layer" },
  },
  // Glow: live states only (charging, driving, the gauge while charging).
  glowChargingBorder: {
    dark: { file: "src/index.css", css: "border-color: oklch(78% 0.15 168 / 0.42)", use: "Now hero border while charging" },
    light: { file: "src/index.css", css: "border-color: oklch(60% 0.14 168 / 0.5)", use: "Now hero border while charging" },
  },
  glowChargingRing: {
    dark: { file: "src/index.css", css: "0 0 0 1px oklch(78% 0.15 168 / 0.16)", use: "1px ring outside the hero while charging" },
    light: { file: "src/index.css", css: "0 0 0 1px oklch(60% 0.14 168 / 0.18)", use: "1px ring outside the hero while charging" },
  },
  glowChargingHalo: {
    dark: { file: "src/index.css", css: "0 0 44px -10px oklch(78% 0.15 168 / 0.4)", use: "soft halo around the hero while charging" },
    light: { file: "src/index.css", css: "0 0 32px -12px oklch(60% 0.14 168 / 0.45)", use: "soft halo around the hero while charging" },
  },
  glowDrivingBorder: {
    dark: { file: "src/index.css", css: "border-color: oklch(76% 0.12 235 / 0.42)", use: "Now hero border while driving" },
    light: { file: "src/index.css", css: "border-color: oklch(58% 0.12 235 / 0.5)", use: "Now hero border while driving" },
  },
  glowDrivingRing: {
    dark: { file: "src/index.css", css: "0 0 0 1px oklch(76% 0.12 235 / 0.16)", use: "1px ring outside the hero while driving" },
    light: { file: "src/index.css", css: "0 0 0 1px oklch(58% 0.12 235 / 0.18)", use: "1px ring outside the hero while driving" },
  },
  glowDrivingHalo: {
    dark: { file: "src/index.css", css: "0 0 44px -10px oklch(76% 0.12 235 / 0.4)", use: "soft halo around the hero while driving" },
    light: { file: "src/index.css", css: "0 0 32px -12px oklch(58% 0.12 235 / 0.45)", use: "soft halo around the hero while driving" },
  },
  glowGauge: {
    dark: { file: "src/index.css", css: "drop-shadow(0 0 5px oklch(78% 0.15 168 / 0.6))", use: "gauge fill glow while charging" },
    light: { file: "src/index.css", css: "drop-shadow(0 0 4px oklch(60% 0.14 168 / 0.5))", use: "gauge fill glow while charging" },
  },
  glowSparkLine: {
    dark: { file: "src/index.css", css: "drop-shadow(0 0 4px oklch(78% 0.15 168 / 0.7))", use: "charging-power sparkline" },
    light: { file: "src/index.css", css: "drop-shadow(0 0 3px oklch(60% 0.14 168 / 0.45))", use: "charging-power sparkline" },
  },
  glowSparkDot: {
    dark: { file: "src/index.css", css: "drop-shadow(0 0 5px oklch(84% 0.14 168 / 0.9))", use: "charging-power sparkline's last point" },
    light: { file: "src/index.css", css: "drop-shadow(0 0 4px oklch(60% 0.14 168 / 0.7))", use: "charging-power sparkline's last point" },
  },
  // ArcGauge's three fills, a 2-stop diagonal gradient each (same in both themes).
  gaugeOkFrom: { both: { file: "src/components/ArcGauge.tsx", css: 'stopColor="oklch(78% 0.15 168)"', use: "gauge fill, normal" } },
  gaugeOkTo: { both: { file: "src/components/ArcGauge.tsx", css: 'stopColor="oklch(70% 0.13 190)"', use: "gauge fill, normal" } },
  gaugeCriticalFrom: { both: { file: "src/components/ArcGauge.tsx", css: 'stopColor="oklch(72% 0.19 25)"', use: "gauge fill, low battery" } },
  gaugeCriticalTo: { both: { file: "src/components/ArcGauge.tsx", css: 'stopColor="oklch(66% 0.2 15)"', use: "gauge fill, low battery" } },
  gaugeIdleFrom: { both: { file: "src/components/ArcGauge.tsx", css: 'stopColor="oklch(62% 0.02 260)"', use: "gauge fill, offline" } },
  gaugeIdleTo: { both: { file: "src/components/ArcGauge.tsx", css: 'stopColor="oklch(55% 0.02 260)"', use: "gauge fill, offline" } },
  // Charts draw with fixed sRGB colours (the same in both themes).
  chartMilesBar: { both: { file: "src/components/charts.tsx", css: 'color = "#10b981"', use: "miles-per-day bars (BarChart default)" } },
  chartSocCharge: { both: { file: "src/components/SocTrendChart.tsx", css: 'const SOC_GREEN = "#10b981"', use: "charge-level chart: the daily band, the end-of-day line and the charging-day ticks" } },
  chartSocDrive: { both: { file: "src/lib/driveTimelineData.ts", css: 'speed: "#38bdf8"', use: "driving (sky): the drive timeline's speed panel" } },
  chartSocLimit: { both: { file: "src/components/SocTrendChart.tsx", css: 'const SOC_LIMIT = "#f59e0b"', use: "charge-level chart: the charge-limit line (faint, solid) and its label" } },
  chartIdleAwakeBar: { both: { file: "src/pages/TruckPage.tsx", css: 'color="#6366f1"', use: "idle-awake bars" } },
  chartIdleAwakeTile: { both: { file: "src/components/IdleAwakeTile.tsx", css: 'fill="#818cf8"', use: "idle-awake tile mini bars (last day full, others at 50%)" } },
  // The share card is a fixed dark 1080 x 1080 image in both themes (lib/shareCard.ts paintShareCard).
  shareCardBackground: { both: { file: "src/lib/shareCard.ts", css: 'g.fillStyle = "#0a0a0a"', use: "share card: background" } },
  shareCardMuted: { both: { file: "src/lib/shareCard.ts", css: 'g.fillStyle = "#a3a3a3"', use: "share card: eyebrow, subtitle and stat labels" } },
  shareCardTitle: { both: { file: "src/lib/shareCard.ts", css: 'g.fillStyle = "#fafafa"', use: "share card: the title figure" } },
  shareCardValue: { both: { file: "src/lib/shareCard.ts", css: 'g.fillStyle = "#e5e5e5"', use: "share card: stat values" } },
  shareCardNote: { both: { file: "src/lib/shareCard.ts", css: 'g.fillStyle = "#fbbf24"', use: "share card: notes (estimated share, not yet priced)" } },
  shareCardFooter: { both: { file: "src/lib/shareCard.ts", css: 'g.fillStyle = "#737373"', use: "share card: footer lines" } },
  // The drive route on a map (components/DriveMap.tsx), the same in both themes: a glow under a line coloured by speed
  // (mph buckets), neutral routed or unknown gaps, and the start and end pins.
  routeGlow: { both: { file: "src/components/DriveMap.tsx", css: 'color: "#10b981",', use: "drive route: the wide faint glow under the line (18%)" } },
  routeLine: { both: { file: "src/components/DriveMap.tsx", css: 'color: "#34d399",', use: "drive route: the line without speeds" } },
  routeSpeedUnder5: { both: { file: "src/components/DriveMap.tsx", css: '{ max: 5, color: "#6b7280", label: "<5" }', use: "drive route: under 5 mph" } },
  routeSpeed5to25: { both: { file: "src/components/DriveMap.tsx", css: '{ max: 25, color: "#06b6d4", label: "5–25" }', use: "drive route: 5 to 25 mph" } },
  routeSpeed25to50: { both: { file: "src/components/DriveMap.tsx", css: '{ max: 50, color: "#34d399", label: "25–50" }', use: "drive route: 25 to 50 mph" } },
  routeSpeed50to65: { both: { file: "src/components/DriveMap.tsx", css: '{ max: 65, color: "#f59e0b", label: "50–65" }', use: "drive route: 50 to 65 mph" } },
  routeSpeed65Plus: { both: { file: "src/components/DriveMap.tsx", css: '{ max: Infinity, color: "#f43f5e", label: "65+" }', use: "drive route: 65 mph and over" } },
  routeGap: { both: { file: "src/components/DriveMap.tsx", css: 'color: "#737373",', use: "drive route: routed (solid) and unknown (dashed) gaps" } },
  routeStart: { both: { file: "src/components/DriveMap.tsx", css: 'circleIcon("#10b981")', use: "drive route: the start pin" } },
  routeEnd: { both: { file: "src/components/DriveMap.tsx", css: 'circleIcon("#f43f5e")', use: "drive route: the end pin" } },
  chargePin: { both: { file: "src/components/DriveMap.tsx", css: 'circleIcon("#f59e0b")', use: "charge map: the charge's pin, the charger square and the dashed link to it" } },
  chargeRecorded: { both: { file: "src/components/DriveMap.tsx", css: "background:#737373;border:1.5px solid #0a0a0a", use: "charge map: the recorded GPS dot once the pin snapped to a charger" } },
  chargePinInk: { both: { file: "src/components/DriveMap.tsx", css: "color:#0a0a0a;font:700 13px", use: "charge map: the bolt on the charger square and the dots' rings" } },
  mapDrive: { both: { file: "src/components/DriveMap.tsx", css: 'color: "#34d399",', use: "activity map: every drive's route (1.5 px at 50%)" } },
  chargeFast: { both: { file: "src/components/DriveMap.tsx", css: 'fast: "#f43f5e",', use: "activity map: a DC fast charge's dot" } },
  chargeL2: { both: { file: "src/components/DriveMap.tsx", css: 'l2: "#06b6d4",', use: "activity map: a level 2 (AC, 3 kW and up) charge's dot" } },
  chargeL1: { both: { file: "src/components/DriveMap.tsx", css: 'l1: "#a3a3a3",', use: "activity map: a level 1 (under 3 kW) charge's dot" } },
  efficiencyBarEnd: { both: { file: "src/components/EfficiencyBars.tsx", css: ",#059669)", use: "efficiency bars: the bottom of a bar's gradient (its top is mapDrive for the best bucket, routeStart for the rest)" } },
  timelineDraw: { both: { file: "src/lib/driveTimelineData.ts", css: 'draw: "#ef4444",', use: "drive timeline: modelled power while drawing (above 0)" } },
  timelineRegen: { both: { file: "src/lib/driveTimelineData.ts", css: 'regen: "#22c55e",', use: "drive timeline: modelled power while recovering (below 0)" } },
  modeConserve: { both: { file: "src/lib/driveTimelineData.ts", css: 'Conserve: "#22c55e",', use: "drive timeline: Conserve drive mode band" } },
  modeAllPurpose: { both: { file: "src/lib/driveTimelineData.ts", css: 'AllPurpose: "#f97316",', use: "drive timeline: All-Purpose drive mode band" } },
  modeSport: { both: { file: "src/lib/driveTimelineData.ts", css: 'Sport: "#ef4444",', use: "drive timeline: Sport drive mode band" } },
  modeOther: { both: { file: "src/lib/driveTimelineData.ts", css: 'Other: "#a3a3a3",', use: "drive timeline: any other drive mode" } },
};

// Non-colour values, each with the source fragment that proves it.
export const SCALAR: Record<string, { value: number | string; file: string; css: string; use: string }> = {
  "type.caption": { value: 11, file: "src/styles/next-type.css", css: "font-size: 0.6875rem", use: "captions, pills, tab labels (px)" },
  "type.subline": { value: 12, file: "node_modules/tailwindcss/theme.css", css: "--text-xs: 0.75rem", use: "sub-lines, labels' neighbours (px)" },
  "type.value": { value: 14, file: "src/styles/next-type.css", css: "font-size: 0.875rem", use: "values, body (text-base/lg fold to 14 px)" },
  "type.title": { value: 24, file: "src/styles/next-type.css", css: "font-size: 1.5rem", use: "titles, KPI figures (px)" },
  "type.hero": { value: 36, file: "src/styles/next-type.css", css: "font-size: 2.25rem", use: "hero numbers (px)" },
  "type.labelSize": { value: 11, file: "src/index.css", css: "font-size: 11px", use: "small-caps label size (.ui-label)" },
  "type.labelTracking": { value: 0.09, file: "src/index.css", css: "letter-spacing: 0.09em", use: "small-caps label tracking (em), uppercase, weight 500" },
  "type.labelWeight": { value: 500, file: "src/index.css", css: "font-weight: 500", use: "small-caps labels, tab labels" },
  "type.numberTracking": { value: -0.012, file: "src/index.css", css: "letter-spacing: -0.012em", use: "tabular numbers (em)" },
  "type.family": { value: "Geist UI", file: "src/index.css", css: "font-family: 'Geist UI'", use: "everything (variable weight 100-900, tabular figures 'tnum', 'cv11')" },
  "type.familyMono": { value: "Geist Mono UI", file: "src/index.css", css: "font-family: 'Geist Mono UI'", use: "hero readouts only (Time left, the kW value)" },
  "space.unit": { value: 4, file: "node_modules/tailwindcss/theme.css", css: "--spacing: 0.25rem", use: "spacing unit (px); cards p-4 = 16, the Now hero p-5 = 20, gaps 8/12/16" },
  "radius.card": { value: 12, file: "node_modules/tailwindcss/theme.css", css: "--radius-xl: 0.75rem", use: "cards (rounded-xl, px)" },
  "radius.inner": { value: 8, file: "node_modules/tailwindcss/theme.css", css: "--radius-lg: 0.5rem", use: "inner panels, inputs (rounded-lg, px)" },
  "motion.ms": { value: 200, file: "src/index.css", css: "box-shadow 200ms ease-out", use: "state transitions (ms, ease-out); none with Reduce Motion" },
  "gauge.sweep": { value: 240, file: "src/components/ArcGauge.tsx", css: "const sweep = 240", use: "arc sweep (degrees), starting at 150° clockwise from 3 o'clock" },
  "gauge.radius": { value: 52, file: "src/components/ArcGauge.tsx", css: "const r = 52", use: "arc radius in a 120×108 viewBox" },
  "gauge.stroke": { value: 7, file: "src/components/ArcGauge.tsx", css: "strokeWidth={7}", use: "track and fill width, round caps" },
  "gauge.trackOpacity": { value: 0.14, file: "src/components/ArcGauge.tsx", css: "strokeOpacity={0.14}", use: "track = textBody at this opacity" },
  "gauge.size": { value: 132, file: "src/components/ArcGauge.tsx", css: "size = 132", use: "default width (pt); height = 0.9 × width" },
  "logo.size": { value: 22, file: "src/layout/AppLayout.tsx", css: '<Logo size={22} className="text-emerald-400" />', use: "header logo size (pt), coloured roles.brandMark" },
  "logo.viewBox": { value: 64, file: "src/components/Logo.tsx", css: 'viewBox="0 0 64 64"', use: "logo drawing space; frame inset 4, 56×56, corner 12" },
  "logo.frameRadius": { value: 12, file: "src/components/Logo.tsx", css: 'rx="12"', use: "logo frame corner radius (viewBox units)" },
  "logo.frameStroke": { value: 3.5, file: "src/components/Logo.tsx", css: 'strokeWidth="3.5"', use: "logo frame stroke (viewBox units), no fill" },
  "logo.frameOpacity": { value: 0.9, file: "src/components/Logo.tsx", css: 'opacity="0.9"', use: "logo frame opacity; the bolt is solid" },
  "logo.wordmarkGap": { value: 8, file: "src/layout/AppLayout.tsx", css: "flex items-center gap-2 font-semibold tracking-tight text-neutral-100", use: "gap between logo and 'Rivolt' (gap-2, pt); wordmark semibold, tracking-tight, roles.textPrimary" },
  "logo.wordmarkTracking": { value: -0.025, file: "node_modules/tailwindcss/theme.css", css: "--tracking-tight: -0.025em", use: "'Rivolt' wordmark tracking (em)" },
  "tabBar.minHeight": { value: 56, file: "src/layout/TabShell.tsx", css: "min-h-14", use: "tab height (pt), plus the home-indicator inset" },
  "tabBar.icon": { value: 22, file: "src/layout/TabShell.tsx", css: "width: 22", use: "icon size (pt), 24×24 viewBox" },
  "tabBar.iconStroke": { value: 1.8, file: "src/layout/TabShell.tsx", css: "strokeWidth: 1.8", use: "icon stroke, round caps and joins" },
  "map.routeWidth": { value: 1.5, file: "src/components/DriveMap.tsx", css: "weight: 1.5,\n        opacity: 0.5,", use: "activity map: every drive's route width (pt)" },
  "map.routeOpacity": { value: 0.5, file: "src/components/DriveMap.tsx", css: "weight: 1.5,\n        opacity: 0.5,", use: "activity map: every drive's route opacity" },
  "map.dot": { value: 10, file: "src/components/DriveMap.tsx", css: "width:10px;height:10px;border-radius:9999px;background:${color};border:1.5px", use: "activity map: a charge's or a drive end's dot diameter (pt)" },
  "map.dotRing": { value: 1.5, file: "src/components/DriveMap.tsx", css: "width:10px;height:10px;border-radius:9999px;background:${color};border:1.5px", use: "activity map: the dark ring around a dot (pt)" },
};

// The tab bar's own icons (24×24 viewBox, stroked, no fill): SVG path data for SwiftUI Paths.
export const ICONS: Record<string, string[]> = {
  now: ["M4 15a8 8 0 1 1 16 0", "M12 15l4-5", "circle 12 15 1"],
  activity: ["circle 5 6 1.5", "circle 5 12 1.5", "circle 5 18 1.5", "M9 6h11M9 12h11M9 18h11"],
  money: ["M6 3h12v18l-3-2-3 2-3-2-3 2z", "M9 8h6M9 12h6M9 16h3"],
  plan: ["M12 21s-6-5.3-6-10a6 6 0 0 1 12 0c0 4.7-6 10-6 10z", "circle 12 11 2"],
  truck: ["M3 16V8h11v8M14 11h4l3 3v2h-7", "circle 7 17 2", "circle 17 17 2"],
};

// The logo's bolt (filled), 64×64 viewBox.
export const BRAND_BOLT = "M36 10 L18 36 H30 L26 54 L46 26 H34 Z";

// ---- Build ------------------------------------------------------------------

const OKLCH = /oklch\([^)]*\)/;

function literalColour(l: Literal): { oklch: string | null; hex: string; alpha: number; css: string; use: string; source: string } {
  const m = OKLCH.exec(l.css);
  if (m) return { ...colour(m[0]), css: l.css, use: l.use, source: l.file };
  const h = /#([0-9a-fA-F]{6})\b/.exec(l.css);
  if (!h) throw new Error(`no colour in ${l.css}`);
  return { oklch: null, hex: `#${h[1].toUpperCase()}`, alpha: 1, css: l.css, use: l.use, source: l.file };
}

export function buildTokens(src: Sources) {
  const problems: string[] = [];
  const has = (file: string, frag: string) => {
    if (!(src[file] ?? "").includes(frag)) problems.push(`${file} no longer contains: ${frag}`);
  };

  const roles: Record<string, unknown> = {};
  for (const [name, r] of Object.entries(ROLES)) {
    const per: Record<string, unknown> = {};
    for (const theme of ["dark", "light"] as Theme[]) {
      const { value, from } = resolveVar(src, r.cssVar, theme);
      per[theme] = { ...colour(value), source: from };
    }
    roles[name] = { cssVar: r.cssVar, use: r.use, ...per };
  }

  const literals: Record<string, unknown> = {};
  for (const [name, per] of Object.entries(LITERALS)) {
    const out: Record<string, unknown> = {};
    for (const [theme, l] of Object.entries(per)) {
      if (!l) continue;
      has(l.file, l.css);
      out[theme] = literalColour(l);
    }
    literals[name] = out;
  }

  const scalars: Record<string, unknown> = {};
  for (const [name, s] of Object.entries(SCALAR)) {
    has(s.file, s.css);
    scalars[name] = { value: s.value, use: s.use, source: s.file };
  }

  for (const paths of Object.values(ICONS)) {
    for (const p of paths) {
      const frag = p.startsWith("circle ") ? (([cx, cy, r]) => `<circle cx="${cx}" cy="${cy}" r="${r}" />`)(p.split(" ").slice(1)) : `d="${p}"`;
      has("src/layout/TabShell.tsx", frag);
    }
  }

  has("src/components/Logo.tsx", `d="${BRAND_BOLT}"`);

  if (problems.length) throw new Error(`design tokens drifted from the web:\n${problems.join("\n")}`);
  return {
    version: 1,
    generatedFrom: "web/src/lib/designTokens.ts (ui_theme_next); regenerate with UPDATE_DESIGN_TOKENS=1 npx vitest run src/lib/designTokens.test.ts",
    roles,
    literals,
    scalars,
    icons: { viewBox: "0 0 24 24", stroke: 1.8, paths: ICONS, source: "src/layout/TabShell.tsx" },
    // The Rivolt mark: a rounded-square frame (scalars logo.*) and this bolt, filled, in a 64×64 viewBox.
    brandBolt: { viewBox: "0 0 64 64", path: BRAND_BOLT, source: "src/components/Logo.tsx" },
    // Where the iPhone differs from the web.
    platform: {
      units: "Web CSS px map 1:1 to iOS points.",
      fonts:
        "The web ships latin-subset woff2 (web/src/assets/fonts/geist-latin-wght-normal.woff2, geist-mono-latin-wght-normal.woff2); iOS needs the variable TTFs Geist[wght].ttf and GeistMono[wght].ttf from the Geist release (github.com/vercel/geist-font). SIL OFL 1.1, no Reserved Font Name: bundling and subsetting are allowed; ship ios/design/LICENSE-Geist.txt with the app.",
      tabBar: "The web pads the tab bar with env(safe-area-inset-bottom); on iOS use the system safe area (home indicator) the same way.",
      colourSpace: "hex values are sRGB, clamped; the web renders OKLCH, which can be slightly more saturated on wide-gamut screens.",
      appearance: "Follow the system appearance (the web's System setting); light and dark values are both given.",
    },
  };
}

export const SOURCE_FILES = [
  "src/lib/driveTimelineData.ts",
  "src/components/EfficiencyBars.tsx",
  "src/index.css",
  "src/styles/next-type.css",
  "src/components/ArcGauge.tsx",
  "src/layout/TabShell.tsx",
  "src/components/charts.tsx",
  "src/components/SocTrendChart.tsx",
  "src/components/IdleAwakeTile.tsx",
  "src/pages/TruckPage.tsx",
  "src/components/Logo.tsx",
  "src/layout/AppLayout.tsx",
  "src/lib/shareCard.ts",
  "src/components/DriveMap.tsx",
  "node_modules/tailwindcss/theme.css",
];
