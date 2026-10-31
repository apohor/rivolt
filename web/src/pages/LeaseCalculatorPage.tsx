import { useState } from "react";
import { Link } from "react-router-dom";
import Logo from "../components/Logo";
import { getConfig } from "../lib/config";
import { KM_PER_MI, leaseCalc, type LeaseCalcInput } from "../lib/leaseCalc";
import { leaseBar } from "../lib/lease";
import { formatMoney } from "../lib/ledger";
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
          <h1 className="text-3xl font-semibold tracking-tight text-neutral-50">Rivian lease mileage calculator</h1>
          <p className="mt-2 text-base leading-relaxed text-neutral-400">
            Enter your lease terms and your odometer to see whether you&apos;re on pace for your mileage allowance and
            what overage could cost. It runs in your browser: nothing you type is sent or stored.
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
            {field("startDate", "startDate", "Lease start date", { type: "date" }, "From your lease agreement.")}
            {field("endDate", "endDate", "Lease end date", { type: "date" }, "The return date.")}
            {field("allowed", "allowedMilesTotal", `${unit === "km" ? "Kilometers" : "Miles"} allowed for the whole lease`, { ...numProps, placeholder: unit === "km" ? "e.g. 57936" : "e.g. 36000" }, `Total, not per year (for example ${unit === "km" ? "57,936 for 3 years at 19,312" : "36,000 for 3 years at 12,000"}).`)}
            {field("startOdo", "startOdometerMi", `Odometer at lease start (${unit})`, { ...numProps, placeholder: "e.g. 12" }, `${unit === "km" ? "Kilometers" : "Miles"} on the truck when the lease began.`)}
            {field("currentOdo", "currentOdometerMi", `Odometer today (${unit})`, { ...numProps, placeholder: unit === "km" ? "e.g. 24000" : "e.g. 15000" }, "Current reading from the dash or the Rivian app.")}
            {field("overage", "overagePerMile", `Overage rate per ${unit === "km" ? "kilometer" : "mile"}`, numProps, "Your contract states it. Enter 0 if there's no charge.")}
          </div>
          <button type="submit" className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-600">
            Calculate
          </button>
        </form>

        {result?.ok && <Result st={result.status} allowed={input.allowedMilesTotal} currency={currency} unit={unit} />}

        <section aria-label="Track it automatically" className="rounded-xl border border-neutral-800 p-4 sm:p-5">
          <h2 className="text-base font-semibold text-neutral-100">Track this automatically</h2>
          <p className="mt-1 text-sm text-neutral-400">
            Rivolt records your drives and charges, projects your lease miles from your actual odometer, and shows what
            each charge cost. Open source, and free during the beta.
          </p>
          <div className="mt-3 flex flex-wrap gap-3">
            {demoEnabled && (
              <Link to="/demo" className="rounded-md border border-neutral-700 px-4 py-2 text-sm font-semibold text-neutral-100 hover:bg-neutral-900">
                Try the demo
              </Link>
            )}
            {hostedDemo && (
              <a href={DEMO_URL} className="rounded-md border border-neutral-700 px-4 py-2 text-sm font-semibold text-neutral-100 hover:bg-neutral-900">
                Try the demo
              </a>
            )}
            <Link to={signupEnabled ? "/signup" : "/login"} className="rounded-md bg-emerald-700 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-600">
              {signupEnabled ? "Create an account" : "Sign in"}
            </Link>
          </div>
          <p className="mt-3 text-xs text-neutral-500">Sign up with Google or GitHub; the beta has an account cap.</p>
        </section>
        <p className="text-xs text-neutral-500">
          Rivolt is unofficial and not affiliated with, endorsed by or sponsored by Rivian. Rivian is a trademark of
          Rivian Automotive, LLC.
        </p>
      </main>
    </div>
  );
}

const nf = (v: number) => Math.round(v).toLocaleString();

// "Where you stand": the numbers in the user's unit (the maths is in miles).
function Result({ st, allowed, currency, unit }: { st: import("../lib/api").LeaseStatus; allowed: number; currency: string; unit: "mi" | "km" }) {
  const k = unit === "km" ? KM_PER_MI : 1;
  const d = (mi: number) => `${nf(mi * k)} ${unit}`;
  const bar = leaseBar(st, allowed);
  const ahead = st.ahead_behind_mi ?? 0;
  const standing = Math.round(Math.abs(ahead) * k) === 0 ? "On plan" : ahead < 0 ? `You're ${d(-ahead)} over plan` : `You're ${d(ahead)} under plan`;
  const projected = st.projected_total_mi;
  const over = st.projected_overage_mi ?? 0;
  const fill = bar.tone === "over" ? "bg-rose-500" : bar.tone === "watch" ? "bg-amber-500" : "bg-emerald-500";
  const budget = st.miles_per_day_budget ?? 0;
  return (
    <section aria-label="Where you stand" data-lease-result className="space-y-3 rounded-xl border border-neutral-800 bg-neutral-900/50 p-4 sm:p-5">
      <h2 className="text-base font-semibold text-neutral-100">Where you stand</h2>
      <div
        role="progressbar"
        aria-label="Lease miles used"
        aria-valuemin={0}
        aria-valuemax={Math.round(allowed * k)}
        aria-valuenow={Math.round((st.used_mi ?? 0) * k)}
        aria-valuetext={`${d(st.used_mi ?? 0)} used of ${d(allowed)}; ${d(st.allowed_to_date_mi)} allowed to date`}
        className="relative h-3 rounded-full bg-neutral-800"
      >
        {bar.projectedPct != null && bar.projectedPct > bar.usedPct && <div className="absolute inset-y-0 left-0 rounded-full bg-neutral-500/40" style={{ width: `${bar.projectedPct}%` }} />}
        <div className={`absolute inset-y-0 left-0 rounded-full ${fill}`} style={{ width: `${bar.usedPct}%` }} />
        <div className="absolute -inset-y-1 w-0.5 rounded-full bg-neutral-100" style={{ left: `${bar.planPct}%` }} title="Where the pro-rata allowance is today" />
      </div>
      <dl className="space-y-3 text-sm">
        <div>
          <dt className="ui-label">{unit === "km" ? "Kilometers" : "Miles"} driven so far</dt>
          <dd data-lc-used className="text-lg font-semibold tabular-nums text-neutral-100">{nf((st.used_mi ?? 0) * k)}</dd>
        </div>
        <div>
          <dt className="ui-label">Allowed by today</dt>
          <dd className="text-neutral-100">
            <span data-lc-allowed className="text-lg font-semibold tabular-nums">{nf(st.allowed_to_date_mi * k)}</span>{" "}
            <span className="text-neutral-400">(pro-rata)</span>
          </dd>
          <dd data-lc-standing className={`font-medium ${ahead < 0 ? "text-amber-300" : "text-emerald-300"}`}>{standing}</dd>
        </div>
        {projected == null ? (
          <div data-lc-too-early>
            <dt className="ui-label">Projected at lease return</dt>
            <dd className="text-neutral-400">Too early to project a pace. Check back after a couple of weeks of driving.</dd>
          </div>
        ) : (
          <>
            <div>
              <dt className="ui-label">Projected at lease return</dt>
              <dd data-lc-projected className="text-lg font-semibold tabular-nums text-neutral-100">{d(projected)}</dd>
              <dd className="text-xs text-neutral-500">Assumes you keep driving at your average pace so far ({unit === "km" ? "kilometers" : "miles"} used ÷ days elapsed).</dd>
            </div>
            <div>
              <dt className="ui-label">Projected overage</dt>
              <dd data-lc-overage className={over > 0 ? "font-semibold text-rose-300" : "font-semibold text-emerald-300"}>
                {over > 0 ? `${d(over)}, about ${formatMoney(st.projected_overage_cost ?? 0, currency)}` : "Projected to finish within your allowance"}
              </dd>
            </div>
          </>
        )}
        <div>
          <dt className="ui-label">To stay within the allowance</dt>
          <dd data-lc-budget className="font-semibold tabular-nums text-neutral-100">
            {budget === 0 ? "Already over the allowance" : `About ${nf(budget * k)} ${unit} a day`}
          </dd>
        </div>
      </dl>
      <p className="text-xs text-neutral-500">
        An estimate from the numbers you entered. Your lease agreement is what counts; check it for how overage is charged.
      </p>
    </section>
  );
}
