import { useState } from "react";
import { Link } from "react-router-dom";
import Logo from "../components/Logo";
import { getConfig } from "../lib/config";
import { EXAMPLE, costCalc, fastPriceShown, workedExample, type CostField, type CostInput } from "../lib/chargingCost";
import { formatMoney } from "../lib/ledger";
import { priceMonth } from "../lib/sampleTrip";
import { useSystemTheme } from "../lib/theme";

// /charging-cost: a public, signed-out page. A worked example for the sample R1S (every figure an example, none a measurement of any
// owner) and a calculator that runs in this page: no API call, no storage, nothing leaves the browser, and the link to the demo is never
// prefilled with anything typed here.
const DEMO_URL = "https://demo.rivolt.dev";
const CURRENCIES = ["USD", "CAD", "EUR", "GBP", "AUD", "NZD", "CHF", "SEK", "NOK", "DKK"] as const;

type Draft = { rate: string; offRate: string; offShare: string; miles: string; eff: string; fastKwh: string; fastPrice: string };
const EMPTY: Draft = { rate: "", offRate: "", offShare: "", miles: String(EXAMPLE.milesPerMonth), eff: String(EXAMPLE.milesPerKwh), fastKwh: "", fastPrice: "" };
const num = (s: string) => (s.trim() === "" ? NaN : Number(s));
const opt = (s: string) => (s.trim() === "" ? undefined : Number(s));

const th = "px-2 py-1.5 text-left text-xs font-medium text-neutral-400";
const td = "px-2 py-1.5 text-sm text-neutral-200";

export default function ChargingCostPage() {
  useSystemTheme(true);
  const [currency, setCurrency] = useState<(typeof CURRENCIES)[number]>("USD");
  const [d, setD] = useState<Draft>(EMPTY);
  const [submitted, setSubmitted] = useState(false);
  const { demoEnabled, signupEnabled } = getConfig().features;
  const hostedDemo = !demoEnabled && /(^|\.)rivolt\.dev$/.test(window.location.hostname);
  const w = workedExample();
  const showFast = fastPriceShown(new Date());
  const $ = (v: number) => formatMoney(v, "USD", 0);
  const month = priceMonth(EXAMPLE.fastPriceMonth);

  const input: CostInput = {
    ratePerKwh: num(d.rate),
    milesPerMonth: num(d.miles),
    milesPerKwh: num(d.eff),
    offpeakRate: opt(d.offRate),
    offpeakSharePct: opt(d.offShare),
    fastKwh: opt(d.fastKwh),
    fastPrice: opt(d.fastPrice),
  };
  const result = submitted ? costCalc(input) : null;
  const errs: Partial<Record<CostField, string>> = result && !result.ok ? result.errors : {};
  const set = (k: keyof Draft) => (e: React.ChangeEvent<HTMLInputElement>) => setD((x) => ({ ...x, [k]: e.target.value }));
  const numProps = { type: "number", inputMode: "decimal", min: 0, step: "any" } as const;
  const field = (k: keyof Draft, ek: CostField, label: string, props: React.InputHTMLAttributes<HTMLInputElement>, hint?: string) => (
    <label className="flex flex-col gap-1 text-sm">
      <span className="text-xs font-medium text-neutral-400">{label}</span>
      <input value={d[k]} onChange={set(k)} aria-invalid={errs[ek] ? true : undefined} aria-describedby={errs[ek] ? `cc-${k}-err` : undefined} className="input" {...props} />
      {errs[ek] ? (
        <span id={`cc-${k}-err`} role="alert" className="text-xs text-rose-300">{errs[ek]}</span>
      ) : hint ? (
        <span className="text-xs text-neutral-500">{hint}</span>
      ) : null}
    </label>
  );

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
      <main className="mx-auto max-w-3xl space-y-8 px-4 py-8 sm:py-12" data-charging-cost>
        <div>
          <h1 className="text-3xl font-semibold tracking-tight text-neutral-50">How much does it cost to charge a Rivian at home?</h1>
          <p className="mt-2 text-[15px] leading-relaxed text-neutral-400">
            It depends on three things you already know or can look up: your electricity rate, how far you drive, and how efficiently the truck turns
            kWh into miles. Below is a worked example for a sample R1S; put your own numbers in the calculator at the bottom and nothing you type leaves
            your browser.
          </p>
        </div>

        <section aria-labelledby="cc-formula" className="space-y-2">
          <h2 id="cc-formula" className="text-xl font-semibold text-neutral-100">The formula</h2>
          <p className="text-sm text-neutral-300">Cost per mile = your rate per kWh ÷ your miles per kWh.</p>
          <p className="text-sm text-neutral-300">Monthly cost = miles driven ÷ miles per kWh × rate per kWh.</p>
          <p className="text-sm leading-relaxed text-neutral-400">
            Two things make the real figure a little higher than the formula: charging losses (the wall meter reads more kWh than the pack stores,
            typically a few percent on Level 2) and parked drain. Rivolt&apos;s own figures use what the truck reports as added energy and label estimates as
            estimates.
          </p>
        </section>

        <section aria-labelledby="cc-example" data-cc-example className="space-y-3">
          <h2 id="cc-example" className="text-xl font-semibold text-neutral-100">Worked example: a sample R1S, home charging only (example figures)</h2>
          <div className="overflow-x-auto rounded-lg border border-neutral-800">
            <table className="w-full">
              <caption className="sr-only">Inputs for the worked example</caption>
              <thead>
                <tr className="border-b border-neutral-800"><th scope="col" className={th}>Input</th><th scope="col" className={th}>Example</th></tr>
              </thead>
              <tbody className="divide-y divide-neutral-900">
                <tr><th scope="row" className={`${td} text-left font-normal`}>Efficiency</th><td className={td}>{EXAMPLE.milesPerKwh} mi/kWh (sample truck)</td></tr>
                <tr><th scope="row" className={`${td} text-left font-normal`}>Miles per month</th><td className={td}>{EXAMPLE.milesPerMonth.toLocaleString()} mi</td></tr>
                <tr><th scope="row" className={`${td} text-left font-normal`}>Home rate</th><td className={td}>${EXAMPLE.homeRate.toFixed(2)} per kWh (example; US residential rates vary widely by utility and plan)</td></tr>
              </tbody>
            </table>
          </div>
          <p className="text-sm text-neutral-300">
            Energy needed: {EXAMPLE.milesPerMonth.toLocaleString()} ÷ {EXAMPLE.milesPerKwh} ≈ {w.kwh} kWh. Cost: {w.kwh} × ${EXAMPLE.homeRate.toFixed(2)} ≈{" "}
            <strong className="text-neutral-100">{$(w.monthly)} per month</strong>, about <strong className="text-neutral-100">{w.centsPerMile} cents per mile</strong> (example).
          </p>
          <p className="text-sm leading-relaxed text-neutral-400">
            Cold weather, roof racks, speed and a trailer all lower miles per kWh; at {EXAMPLE.coldMilesPerKwh} mi/kWh the same month needs about {w.coldKwh} kWh and costs about{" "}
            {$(w.coldMonthly)} at the same rate (example).
          </p>
        </section>

        <section aria-labelledby="cc-offpeak" className="space-y-2">
          <h2 id="cc-offpeak" className="text-xl font-semibold text-neutral-100">What off-peak hours change (example)</h2>
          <p className="text-sm leading-relaxed text-neutral-300">
            Many utilities sell cheaper electricity at night. If the same {w.kwh} kWh were charged entirely in an off-peak window at ${EXAMPLE.offpeakRate.toFixed(2)} per kWh
            (example), the month would cost about <strong className="text-neutral-100">{$(w.offpeakMonthly)} instead of {$(w.monthly)}</strong>. If only half of it lands in the window, about{" "}
            {$(w.halfOffpeakMonthly)}. The lever is the share of your charging that happens in the window, which is what a charging schedule in the truck controls and what
            Rivolt&apos;s charging history shows you month by month.
          </p>
        </section>

        {showFast && (
          <section aria-labelledby="cc-fast" data-cc-fast className="space-y-2">
            <h2 id="cc-fast" className="text-xl font-semibold text-neutral-100">What one fast-charge stop adds (example)</h2>
            <p className="text-sm leading-relaxed text-neutral-300">
              Fast charging on a road trip costs more per kWh than home charging. On the sample two-day Los Angeles to Las Vegas trip, the planner&apos;s one stop at Barstow
              charges from 58% to 80%, about {EXAMPLE.fastStopKwh} kWh. At a typical price seen by{" "}
              <a href="https://dcfctracker.com/" target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-100">DCFC Tracker</a>{" "}
              for Rivian vehicles on the Rivian Adventure Network in {month}, about ${EXAMPLE.fastPrice.toFixed(2)} per kWh, that stop is roughly{" "}
              <strong className="text-neutral-100">{$(w.fastStop)}</strong> (example; the price at the charger may differ). The same energy at the example home rate would be about{" "}
              {$(w.fastStopAtHome)}. Over a year, the share of your kWh that comes from fast charging is usually what moves the total most.
            </p>
          </section>
        )}

        <section aria-labelledby="cc-year" className="space-y-3">
          <h2 id="cc-year" className="text-xl font-semibold text-neutral-100">Putting it together (example year)</h2>
          <div className="overflow-x-auto rounded-lg border border-neutral-800">
            <table className="w-full">
              <caption className="sr-only">An example year of charging</caption>
              <thead>
                <tr className="border-b border-neutral-800"><th scope="col" className={th}>Example</th><th scope="col" className={`${th} text-right`}>kWh</th><th scope="col" className={`${th} text-right`}>Cost (example)</th></tr>
              </thead>
              <tbody className="divide-y divide-neutral-900 tabular-nums">
                <tr><th scope="row" className={`${td} text-left font-normal`}>Home, standard rate, {EXAMPLE.homeMonths} months × {w.kwh} kWh</th><td className={`${td} text-right`}>{w.yearKwh.toLocaleString()}</td><td className={`${td} text-right`}>{$(w.yearHome)}</td></tr>
                <tr><th scope="row" className={`${td} text-left font-normal`}>Home, same months if all off-peak at ${EXAMPLE.offpeakRate.toFixed(2)}</th><td className={`${td} text-right`}>{w.yearKwh.toLocaleString()}</td><td className={`${td} text-right`}>{$(w.yearOffpeak)}</td></tr>
                {showFast && (
                  <tr><th scope="row" className={`${td} text-left font-normal`}>Fast charging, {EXAMPLE.roadTripStops} road-trip stops × {EXAMPLE.fastStopKwh} kWh at ${EXAMPLE.fastPrice.toFixed(2)}</th><td className={`${td} text-right`}>{w.fastYearKwh}</td><td className={`${td} text-right`}>{$(w.fastYear)}</td></tr>
                )}
              </tbody>
            </table>
          </div>
          <p className="text-sm leading-relaxed text-neutral-400">
            Example only: your rate, your efficiency and how often you fast-charge decide the real number. Rivolt records each charge your truck reports, prices it at your own rates and
            shows where every figure came from.
          </p>
        </section>

        <section aria-labelledby="cc-calc" className="space-y-3">
          <h2 id="cc-calc" className="text-xl font-semibold text-neutral-100">Try it with your numbers</h2>
          <form
            noValidate
            aria-label="Your numbers"
            className="space-y-4 rounded-xl border border-neutral-800 bg-neutral-900/50 p-4 sm:p-5"
            onSubmit={(e) => {
              e.preventDefault();
              setSubmitted(true);
            }}
          >
            <label className="flex items-center gap-2 text-xs font-medium text-neutral-400">
              Currency
              <select value={currency} onChange={(e) => setCurrency(e.target.value as (typeof CURRENCIES)[number])} className="input !w-auto">
                {CURRENCIES.map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </select>
            </label>
            <div className="grid gap-3 sm:grid-cols-2">
              {field("rate", "ratePerKwh", "Your rate per kWh", { ...numProps, placeholder: "your rate" }, "From your electricity bill.")}
              {field("miles", "milesPerMonth", "Miles per month", numProps)}
              {field("eff", "milesPerKwh", "Miles per kWh", numProps, "2.3 is the sample truck.")}
              {field("offRate", "offpeakRate", "Off-peak rate per kWh (optional)", numProps)}
              {field("offShare", "offpeakSharePct", "Share of your charging in off-peak hours, % (optional)", { ...numProps, max: 100 })}
              {field("fastKwh", "fastKwh", "Fast-charge kWh per month (optional)", numProps)}
              {field("fastPrice", "fastPrice", "Fast-charge price per kWh (optional)", numProps, "Your own price; nothing is filled in for you.")}
            </div>
            <button type="submit" className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-600">
              Calculate
            </button>
          </form>
          {result?.ok && (
            <div role="region" aria-label="Your result" data-cc-result className="space-y-2 rounded-xl border border-neutral-800 bg-neutral-900/50 p-4 sm:p-5">
              <dl className="space-y-2 text-sm">
                <div>
                  <dt className="ui-label">Monthly cost</dt>
                  <dd data-cc-total className="text-lg font-semibold tabular-nums text-neutral-100">{formatMoney(result.total, currency)}</dd>
                </div>
                <div>
                  <dt className="ui-label">Per mile</dt>
                  <dd data-cc-permile className="font-semibold tabular-nums text-neutral-100">{(result.perMile * 100).toFixed(1)} cents</dd>
                </div>
                <div>
                  <dt className="ui-label">Energy</dt>
                  <dd className="tabular-nums text-neutral-300">{Math.round(result.totalKwh)} kWh a month{result.fastKwh > 0 ? `, ${Math.round(result.fastKwh)} of it fast charging` : ""}</dd>
                </div>
                {result.saving != null && (
                  <div>
                    <dt className="ui-label">Off-peak saving</dt>
                    <dd data-cc-saving className="font-semibold tabular-nums text-emerald-300">about {formatMoney(result.saving, currency)} a month (estimate)</dd>
                  </div>
                )}
              </dl>
              <p className="text-xs text-neutral-500">An estimate from the numbers you entered. Charging losses and parked drain make the real figure a little higher.</p>
            </div>
          )}
        </section>

        <section aria-label="Track it from your truck" className="rounded-xl border border-neutral-800 p-4 sm:p-5">
          <h2 className="text-base font-semibold text-neutral-100">Want this from your truck&apos;s real charges instead of a formula?</h2>
          <p className="mt-1 text-sm text-neutral-400">Rivolt records them and prices each one at your rate. Sign up (free while in beta) or look at the demo truck first.</p>
          <div className="mt-3 flex flex-wrap gap-3">
            {demoEnabled && (
              <Link to="/demo" className="rounded-md border border-neutral-700 px-4 py-2 text-sm font-semibold text-neutral-100 hover:bg-neutral-900">Try the demo</Link>
            )}
            {hostedDemo && (
              <a href={DEMO_URL} className="rounded-md border border-neutral-700 px-4 py-2 text-sm font-semibold text-neutral-100 hover:bg-neutral-900">Try the demo</a>
            )}
            <Link to={signupEnabled ? "/signup" : "/login"} className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-600">
              {signupEnabled ? "Create an account" : "Sign in"}
            </Link>
          </div>
        </section>
        <p className="text-xs text-neutral-500">
          Unofficial, not affiliated with Rivian. Rivian is a trademark of Rivian Automotive, LLC. Figures on this page are examples, not measurements of any owner&apos;s truck.
          {showFast && <> Fast-charge price: typical price seen by DCFC Tracker, {month}; the price at the charger may differ.</>}
        </p>
      </main>
    </div>
  );
}
