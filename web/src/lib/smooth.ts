// Smoothing utilities for irregularly-sampled time series.
//
// Rivolt's telemetry comes in at variable intervals (5–60s apart).
// A flat moving average treats every neighbor equally regardless of
// time distance — over-smooths dense runs, under-smooths sparse ones,
// and flattens genuine peaks.

export interface Pt {
  x: number; // epoch ms
  y: number;
}

// Gaussian-weighted moving average over a *time* window.
// `sigmaMs` = 1 standard deviation in milliseconds. Points within ±3σ
// contribute meaningfully; we cap the kernel there for speed (~99.7%
// of Gaussian weight). Preserves peaks better than a flat MA and
// handles irregular sampling correctly.
//
// Two-pointer sliding window gives amortized O(n) traversal: for each
// center i, [lo, hi) covers points within ±cutoff; both pointers
// advance monotonically as i moves forward.
export function smoothGaussianTime(pts: Pt[], sigmaMs: number): Pt[] {
  if (pts.length < 3 || sigmaMs <= 0) return pts;
  const cutoff = sigmaMs * 3;
  const twoSigmaSq = 2 * sigmaMs * sigmaMs;
  const n = pts.length;
  const out = new Array<Pt>(n);

  let lo = 0;
  let hi = 0;
  for (let i = 0; i < n; i++) {
    const t = pts[i].x;
    while (lo < n && pts[lo].x < t - cutoff) lo++;
    if (hi < lo) hi = lo;
    while (hi < n && pts[hi].x <= t + cutoff) hi++;

    let wSum = 0;
    let ySum = 0;
    for (let j = lo; j < hi; j++) {
      const dt = pts[j].x - t;
      const w = Math.exp(-(dt * dt) / twoSigmaSq);
      wSum += w;
      ySum += w * pts[j].y;
    }
    out[i] = { x: t, y: wSum > 0 ? ySum / wSum : pts[i].y };
  }
  return out;
}

// Centered flat-window moving average. Kept for callers that want
// index-based (not time-based) smoothing.
export function smoothSeries(
  pts: { x: number; y: number }[],
  window: number,
): { x: number; y: number }[] {
  if (pts.length < 3 || window < 2) return pts;
  const half = Math.floor(window / 2);
  const out = new Array(pts.length);
  for (let i = 0; i < pts.length; i++) {
    const lo = Math.max(0, i - half);
    const hi = Math.min(pts.length - 1, i + half);
    let sum = 0;
    for (let j = lo; j <= hi; j++) sum += pts[j].y;
    out[i] = { x: pts[i].x, y: sum / (hi - lo + 1) };
  }
  return out;
}
