import { useState } from "react";
import { Link } from "react-router-dom";
import Logo from "../components/Logo";
import { LeaseBody } from "../components/LeaseCard";
import { getConfig } from "../lib/config";
import { KM_PER_MI, leaseCalc, type LeaseCalcInput } from "../lib/leaseCalc";
import { usePreferences } from "../lib/preferences";
import { useSystemTheme } from "../lib/theme";

// /lease-calculator: a public, signed-out lease mileage calculator. Everything is computed in this
// page from what the visitor types (lib/leaseCalc.ts mirrors the server's lease math): no API call,
// no storage, nothing leaves the browser.
const DEMO_URL = "https://demo.rivolt.dev";
const CURRENCIES = ["USD", "CAD", "EUR", "GBP", "AUD", "NZD", "CHF", "SEK", "NOK", "DKK"] as const;

type Draft = { startDate: string; endDate: string; allowed: string; startOdo: string; currentOdo: string; overage: string };
const EMPTY: Draft = { startDate: "", endDate: "", allowed: "", startOdo: "", currentOdo: "", overage: "" };

const num = (s: string) => (s.trim() === "" ? NaN : Number(s));

export default function LeaseCalculatorPage() {
  useSystemTheme(true);
  const { distanceUnit } = usePreferences();
  const [unit, setUnit] = useState<"mi" | "km">(distanceUnit);
  const [currency, setCurrency] = useState<(typeof CURRENCIES)[number]>("USD");
  const [d, setD] = useState<Draft>(EMPTY);
  const [submitted, setSubmitted] = useState(false);
  const { demoEnabled, signupEnabled } = getConfig().features;
  const hostedDemo = !demoEnabled && /(^|\.)rivolt\.dev$/.test(window.location.hostname);

  // The page works in the unit typed; the maths in miles (a kilometer rate is per kilometer).
  const toMi = (v: number) => (unit === "km" ? v / KM_PER_MI : v);
  const input: LeaseCalcInput = {
    startDate: d.startDate,
    endDate: d.endDate,
    allowedMilesTotal: toMi(num(d.allowed)),
    startOdometerMi: toMi(num(d.startOdo)),
    currentOdometerMi: toMi(num(d.currentOdo)),
    overagePerMile: unit === "km" ? num(d.overage) * KM_PER_MI : num(d.overage),
  };
  const result = submitted ? leaseCalc(input, new Date()) : null;
  const errs = result && !result.ok ? result.errors : {};
  const set = (k: keyof Draft) => (e: React.ChangeEvent<HTMLInputElement>) => setD((x) => ({ ...x, [k]: e.target.value }));
  const err = (k: keyof LeaseCalcInput) => errs[k];

  const field = (k: keyof Draft, ek: keyof LeaseCalcInput, label: string, props: React.InputHTMLAttributes<HTMLInputElement>, hint?: string) => (
    <label className="flex flex-col gap-1 text-sm">
      <span className="text-xs font-medium text-neutral-400">{label}</span>
      <input
        value={d[k]}
        onChange={set(k)}
        aria-invalid={err(ek) ? true : undefined}
        aria-describedby={err(ek) ? `lc-${k}-err` : undefined}
        className="input"
        {...props}
      />
      {err(ek) ? (
        <span id={`lc-${k}-err`} role="alert" className="text-xs text-rose-300">
          {err(ek)}
        </span>
      ) : hint ? (
        <span className="text-xs text-neutral-500">{hint}</span>
      ) : null}
    </label>
  );
  const word = unit === "km" ? "kilometers" : "miles";
  const numProps = { type: "number", inputMode: "decimal", min: 0, step: "any" } as const;

  return (
    <div className="min-h-full bg-neutral-950 text-neutral-200">
      <header className="border-b border-neutral-900">
        <div className="mx-auto flex max-w-3xl items-center justify-between gap-3 px-4 py-3">
          <Link to="/" aria-label="Rivolt home" className="inline-flex items-center gap-2 text-sm font-semibold text-neutral-100">
            <Logo />
          </Link>
          <nav aria-label="Site" className="flex items-center gap-4 text-sm">
            <Link to="/privacy" className="text-neutral-400 hover:text-neutral-100">Privacy</Link>
            <Link to="/" className="text-neutral-400 hover:text-neutral-100">Back to Rivolt</Link>
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-3xl space-y-6 px-4 py-8 sm:py-12" data-lease-calculator>
        <div>
          <h1 className="text-3xl font-semibold tracking-tight text-neutral-50">Lease mileage calculator</h1>
          <p className="mt-2 text-[15px] leading-relaxed text-neutral-400">
            See whether you are on track for your lease&apos;s mileage allowance, where you will be at return, and what
            the overage could cost. It runs in your browser: nothing you enter is sent or stored.
          </p>
        </div>

        <form
          noValidate
          aria-label="Lease details"
          className="space-y-4 rounded-xl border border-neutral-800 bg-neutral-900/50 p-4 sm:p-5"
          onSubmit={(e) => {
            e.preventDefault();
            setSubmitted(true);
          }}
        >
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
            <div role="group" aria-label="Distance unit" className="inline-flex rounded-md border border-neutral-800 p-0.5 text-xs">
              {(["mi", "km"] as const).map((u) => (
                <button
                  key={u}
                  type="button"
                  aria-pressed={unit === u}
                  onClick={() => setUnit(u)}
                  className={`rounded px-2.5 py-1 ${unit === u ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
                >
                  {u === "mi" ? "Miles" : "Kilometers"}
                </button>
              ))}
            </div>
            <label className="flex items-center gap-2 text-xs font-medium text-neutral-400">
              Currency
              <select value={currency} onChange={(e) => setCurrency(e.target.value as (typeof CURRENCIES)[number])} className="input !w-auto">
                {CURRENCIES.map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </select>
            </label>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            {field("startDate", "startDate", "Lease start date", { type: "date" })}
            {field("endDate", "endDate", "Lease end date", { type: "date" })}
            {field("allowed", "allowedMilesTotal", `${unit === "km" ? "Kilometers" : "Miles"} allowed over the whole lease`, { ...numProps, placeholder: unit === "km" ? "e.g. 57936" : "e.g. 36000" })}
            {field("startOdo", "startOdometerMi", `Odometer at lease start (${unit})`, { ...numProps, placeholder: "e.g. 12" })}
            {field("currentOdo", "currentOdometerMi", `Odometer now (${unit})`, { ...numProps, placeholder: unit === "km" ? "e.g. 24000" : "e.g. 15000" })}
            {field("overage", "overagePerMile", `Overage per ${unit === "km" ? "kilometer" : "mile"}`, { ...numProps, placeholder: "e.g. 0.25" }, `What the lessor charges for each ${unit === "km" ? "kilometer" : "mile"} over; 0 if none.`)}
          </div>
          <button type="submit" className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-600">
            Calculate
          </button>
        </form>

        {result?.ok && (
          <section aria-label="Result" data-lease-result className="space-y-2">
            <LeaseBody st={result.status} allowed={input.allowedMilesTotal} currency={currency} end={d.endDate} unit={unit} />
            {result.status.projected_total_mi == null && (
              <p data-lease-too-early className="text-sm text-neutral-400">
                A projection needs at least two weeks of the lease behind you, so only today&apos;s standing and your daily
                budget in {word} are shown.
              </p>
            )}
            <p className="text-xs text-neutral-500">
              An estimate: it assumes you keep driving at the average pace since the lease began. Your lease contract
              decides what counts.
            </p>
          </section>
        )}

        <section aria-label="Track it automatically" className="rounded-xl border border-neutral-800 p-4 sm:p-5">
          <h2 className="text-base font-semibold text-neutral-100">Track this automatically with Rivolt</h2>
          <p className="mt-1 text-sm text-neutral-400">
            Rivolt reads your odometer from your Rivian and keeps this up to date, along with charging cost, trips and
            efficiency. It is free and open source.
          </p>
          <div className="mt-3 flex flex-wrap gap-3">
            {demoEnabled && (
              <Link to="/demo" className="rounded-md border border-neutral-700 px-4 py-2 text-sm font-semibold text-neutral-100 hover:bg-neutral-900">
                Try the demo truck
              </Link>
            )}
            {hostedDemo && (
              <a href={DEMO_URL} className="rounded-md border border-neutral-700 px-4 py-2 text-sm font-semibold text-neutral-100 hover:bg-neutral-900">
                Try the demo truck
              </a>
            )}
            <Link to={signupEnabled ? "/signup" : "/login"} className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-600">
              {signupEnabled ? "Create an account" : "Sign in"}
            </Link>
          </div>
        </section>
      </main>
    </div>
  );
}
