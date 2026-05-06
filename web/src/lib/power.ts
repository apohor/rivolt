// Shared physics-based power model for drive samples.
//
// Why not derive from SoC delta? Because Rivian reports BatteryLevelPct
// at 1% resolution (see internal/rivian/ws.go — vs.BatteryLevel.Value
// is integer percent). On a 135 kWh pack that's 1.35 kWh per step. A
// 5-second hard regen at 50 kW recovers ~0.07 kWh = 5% of one step,
// so most braking events don't move the SoC integer at all and are
// invisible to the derivative. Aggressive smoothing (the only way to
// get *any* signal out of the 1%-integer SoC) then averages whatever
// peaks survive across ~60 s and they vanish.
//
// Instead: model the longitudinal force balance on the vehicle from
// speed, acceleration, road grade, and aero/rolling drag. Multiply by
// speed to get power at the wheels, divide/multiply by drivetrain
// efficiency to get battery-side power. The constants (mass, Cd,
// frontal area) are R1S-ish defaults; the calibration step at the end
// rescales the whole series to match drive.EnergyUsedKWh integrated
// over time, which absorbs per-vehicle variation (R1T trim, aftermarket
// accessories, cargo, headwind that wasn't in the weather data, etc.)
// without hand-tuning every constant.
//
// Convention: positive = draw, negative = regen.

import type { Drive, Sample } from "./api";
import { smoothGaussianTime } from "./smooth";

export type PowerAnalysis = {
  // Time-series power in kW (positive = draw, negative = regen),
  // smoothed and calibrated to drive.EnergyUsedKWh.
  powerPts: { x: number; y: number }[];
  // Smoothed elevation series in feet, also reused as the Battery
  // panel's backdrop in the timeline. Empty when no samples carry
  // altitude (legacy ElectraFi rows, recorder offline, cold-cache
  // misses against the DEM).
  elevPts: { x: number; y: number }[];
  // Total energy drawn from the pack across the drive (kWh).
  drawKwh: number;
  // Total energy recovered via regen (kWh, always ≥ 0).
  regenKwh: number;
  // Regen as a percentage of consumption — 0 when there's no usable
  // signal. Typical EV drives sit in the 10–30 % range; downhill or
  // city stop-and-go can push above 40 %.
  regenPct: number;
};

export function analyzeDrivePower(
  samples: Sample[],
  drive: Drive,
): PowerAnalysis {
  const elevPts = buildElevPts(samples);
  const powerPts = derivePower(samples, elevPts, drive.EnergyUsedKWh);
  let drawKwh = 0;
  let regenKwh = 0;
  for (let i = 1; i < powerPts.length; i++) {
    const dtH = (powerPts[i].x - powerPts[i - 1].x) / 3_600_000;
    const avg = (powerPts[i].y + powerPts[i - 1].y) / 2;
    if (avg >= 0) drawKwh += avg * dtH;
    else regenKwh += -avg * dtH;
  }
  const regenPct = drawKwh > 0.1 ? (regenKwh / drawKwh) * 100 : 0;
  return { powerPts, elevPts, drawKwh, regenKwh, regenPct };
}

export function buildElevPts(samples: Sample[]): { x: number; y: number }[] {
  const raw = samples
    .filter(
      (p) =>
        typeof p.altitude_m === "number" && Number.isFinite(p.altitude_m),
    )
    .map((p) => ({
      x: new Date(p.At).getTime(),
      y: (p.altitude_m as number) * 3.28084,
    }));
  return smoothGaussianTime(raw, 15_000);
}

export function derivePower(
  samples: Sample[],
  elevPts: { x: number; y: number }[],
  totalEnergyKwh: number,
): { x: number; y: number }[] {
  if (samples.length < 3) return [];

  // Vehicle constants. Roughly R1S Large pack with passengers + gear.
  // The calibration loop below scales the final series so being off
  // by ±20 % on any of these is harmless — it just changes the scale
  // factor.
  const MASS_KG = 3050;
  const CD = 0.34;
  const FRONTAL_AREA_M2 = 3.0;
  const RHO_KG_M3 = 1.225;
  const CRR = 0.012;
  const G_M_S2 = 9.81;
  const ETA_DRIVE = 0.85; // wheel→battery losses while drawing
  const ETA_REGEN = 0.7; // wheel→battery losses while recovering
  const ACCESSORY_KW = 1.2; // HVAC + electronics baseline

  const elevAt = (t: number): number => {
    if (elevPts.length === 0) return 0;
    let best = elevPts[0];
    let bestD = Math.abs(best.x - t);
    for (let i = 1; i < elevPts.length; i++) {
      const d = Math.abs(elevPts[i].x - t);
      if (d < bestD) {
        bestD = d;
        best = elevPts[i];
      }
    }
    return best.y / 3.28084; // ft → m
  };

  const points = samples.map((s) => ({
    t: new Date(s.At).getTime(),
    v: (s.SpeedMph || 0) * 0.44704, // mph → m/s
    elev: elevAt(new Date(s.At).getTime()),
  }));

  const out: { x: number; y: number }[] = [];
  for (let i = 1; i < points.length; i++) {
    const dtSec = (points[i].t - points[i - 1].t) / 1000;
    // Skip large telemetry gaps (parked at a charger mid-window etc.) —
    // a 60 s gap can produce a deceptively huge dv/dt.
    if (dtSec <= 0 || dtSec > 30) continue;

    const v = (points[i].v + points[i - 1].v) / 2;
    const dvdt = (points[i].v - points[i - 1].v) / dtSec;
    const dist = v * dtSec;
    const dh = points[i].elev - points[i - 1].elev;
    // Use grade only when we actually moved. At rest dh/dist is
    // numerically explosive and the resulting "grade" force would be
    // nonsense.
    const grade = dist > 0.5 ? dh / dist : 0;

    const fAir = 0.5 * CD * FRONTAL_AREA_M2 * RHO_KG_M3 * v * v;
    const fRoll = v > 0.5 ? CRR * MASS_KG * G_M_S2 : 0;
    const fGrade = MASS_KG * G_M_S2 * grade; // small-angle approx
    const fAccel = MASS_KG * dvdt;
    const fTotal = fAir + fRoll + fGrade + fAccel;

    const pWheelsW = fTotal * v;
    // Battery-side: drive losses inflate consumption; regen losses
    // reduce captured energy.
    const pBatteryW =
      pWheelsW >= 0
        ? pWheelsW / ETA_DRIVE + ACCESSORY_KW * 1000
        : pWheelsW * ETA_REGEN + ACCESSORY_KW * 1000;
    out.push({
      x: (points[i].t + points[i - 1].t) / 2,
      y: pBatteryW / 1000, // W → kW
    });
  }

  // Calibrate to the drive's known net energy. Integrate the modeled
  // power, compare to drive.EnergyUsedKWh (which the backend computed
  // from SoC delta × usable pack capacity at drive close), and rescale
  // uniformly. Clamped to [0.5, 2.0] so a noisy energy total can't
  // produce a wildly distorted ribbon.
  if (totalEnergyKwh > 0 && out.length > 1) {
    let energyKwh = 0;
    for (let i = 1; i < out.length; i++) {
      const dtH = (out[i].x - out[i - 1].x) / 3_600_000;
      const avg = (out[i].y + out[i - 1].y) / 2;
      energyKwh += avg * dtH;
    }
    if (energyKwh > 0.1) {
      const factor = Math.max(
        0.5,
        Math.min(2.0, totalEnergyKwh / energyKwh),
      );
      for (const p of out) p.y *= factor;
    }
  }

  // Light smoothing: 4 s window cleans per-sample dvdt jitter without
  // erasing brake events, which last 5–15 s and are the whole point of
  // the ribbon.
  return smoothGaussianTime(out, 4_000);
}
