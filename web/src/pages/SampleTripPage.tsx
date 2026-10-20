import { useMemo, type ReactNode } from "react";
import Logo from "../components/Logo";
import { formatMoney } from "../lib/ledger";
import { fmtRange, safeTrackerUrl } from "../lib/planCost";
import { fmtDuration, priceMonth, readSampleTrip, safeDemoUrl, safeSignupUrl, readSampleTrips, routePath, type SampleDay, type SampleStop, type SampleTrip, type SampleTripCard } from "../lib/sampleTrip";
import { useSystemTheme } from "../lib/theme";

// /sample-trips and /sample-trips/<slug>: public pages that show the trip planner on three sample trips. The
// server renders the plan once and puts it in the page as JSON, so there is no API call here, and the links are
// plain anchors (a full page load) so a visitor who arrives from another page also gets that JSON. The server's own
// article is what crawlers and no-JS readers see; this component replaces it on mount.

const linkCls = "text-emerald-300 underline underline-offset-2 hover:text-emerald-200";
const btn = "inline-flex items-center rounded-md border border-neutral-700 px-4 py-2 text-sm font-medium text-neutral-100 hover:border-neutral-500";
const btnPrimary = "inline-flex items-center rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500";

function Frame({ children }: { children: ReactNode }) {
  useSystemTheme(true);
  return (
    <div className="min-h-full bg-neutral-950 text-neutral-200">
      <header className="border-b border-neutral-900">
        <div className="mx-auto flex max-w-3xl items-center justify-between gap-3 px-4 py-3">
          <a href="/" aria-label="Rivolt home" className="inline-flex items-center gap-2 text-sm font-semibold text-neutral-100">
            <Logo />
          </a>
          <nav aria-label="Sample trips" className="flex items-center gap-4 text-sm">
            <a href="/sample-trips" className="text-neutral-400 hover:text-neutral-100">Sample trips</a>
            <a href="/signup" className="text-neutral-400 hover:text-neutral-100">Sign up</a>
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-3xl space-y-6 px-4 py-8 sm:py-12">{children}</main>
      <footer className="border-t border-neutral-900 text-xs text-neutral-500">
        <div className="mx-auto max-w-3xl px-4 py-4">Unofficial, not affiliated with Rivian.</div>
      </footer>
    </div>
  );
}

function Missing() {
  return (
    <Frame>
      <h1 className="text-2xl font-semibold text-neutral-50">This sample trip didn&apos;t load</h1>
      <p className="text-neutral-400">
        Open the <a href="/sample-trips" className={linkCls}>list of sample trips</a> to pick one.
      </p>
    </Frame>
  );
}

// The tracker is the source of the prices: its name links to the report when the page gives one, else to the tracker's own site
// (only ever its own https host), and says the month the prices were seen.
const TRACKER = "DCFC Tracker";
function TrackerLink({ url }: { url?: string }) {
  return (
    <a href={safeTrackerUrl(url) ?? "https://dcfctracker.com/"} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-200">
      {TRACKER}
    </a>
  );
}
// "Typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ." with the name as a link.
function PriceNote({ text, url }: { text: string; url?: string }) {
  const i = text.indexOf(TRACKER);
  if (i < 0) return <>{text}</>;
  return (
    <>
      {text.slice(0, i)}
      <TrackerLink url={url} />
      {text.slice(i + TRACKER.length)}
    </>
  );
}

const stat = "rounded-lg border border-neutral-800 bg-neutral-900/50 px-3 py-2";

function RouteMap({ route, title, stops }: { route: [number, number][]; title: string; stops: SampleStop[] }) {
  const p = useMemo(() => routePath(route), [route]);
  if (!p) return null;
  return (
    <svg viewBox="0 0 600 360" role="img" aria-label={`Route of ${title}`} className="w-full rounded-lg border border-neutral-800 bg-neutral-900/50">
      <path d={p.d} fill="none" stroke="#34d399" strokeWidth="3" strokeLinejoin="round" strokeLinecap="round" />
      {stops.filter((s) => typeof s.lat === "number" && typeof s.lon === "number").map((s, i) => {
        const [x, y] = p.at(s.lat as number, s.lon as number);
        return <circle key={`${s.name}-${i}`} cx={x} cy={y} r="5" fill="#fbbf24" stroke="#0a0a0a" strokeWidth="1.5" />;
      })}
      <circle cx={p.start[0]} cy={p.start[1]} r="6" fill="#e5e5e5" />
      <circle cx={p.end[0]} cy={p.end[1]} r="6" fill="#34d399" stroke="#e5e5e5" strokeWidth="2" />
    </svg>
  );
}

function Day({ d, currency }: { d: SampleDay; currency: string }) {
  return (
    <section className="space-y-3 border-t border-neutral-900 pt-5" aria-label={d.label}>
      <h3 className="text-lg font-semibold text-neutral-50">{d.label}</h3>
      <p className="text-sm text-neutral-300">
        {d.from} to {d.to}: {Math.round(d.distance_mi)} mi, {fmtDuration(d.drive_min)} driving, arriving with {Math.round(d.arrival_soc)}%.
      </p>
      {d.stops.length > 0 && (
        <ul className="space-y-2">
          {d.stops.map((s, i) => (
            <li key={`${s.name}-${i}`} className="rounded-lg border border-neutral-800 px-3 py-2 text-sm">
              <div className="font-medium text-neutral-100">{s.name}</div>
              <div className="text-neutral-400">
                {s.network} · arrive {Math.round(s.arrive_soc)}%, leave {Math.round(s.depart_soc)}% · {Math.round(s.charge_min)} min
                {s.max_kw > 0 ? ` · up to ${Math.round(s.max_kw)} kW` : ""}
                {s.cost_low != null && s.cost_high != null ? ` · about ${fmtRange(s.cost_low, s.cost_high, (v) => formatMoney(v, currency))} (estimate)` : " · price shown at the charger"}
              </div>
              {s.price_note && <div className="text-xs text-neutral-500"><PriceNote text={s.price_note} url={s.price_url} /></div>}
            </li>
          ))}
        </ul>
      )}
      {d.overnight && (
        <p className="rounded-lg border border-neutral-800 px-3 py-2 text-sm text-neutral-300">
          Overnight at {d.overnight.name}: {d.overnight.nights && d.overnight.nights > 1 ? `${d.overnight.nights} nights, ` : ""}parked about {Math.round(d.overnight.parked_hours)} h on Level 2 at {d.overnight.l2_kw} kW, adding about {Math.round(d.overnight.added_kwh)} kWh, to{" "}
          {Math.round(d.overnight.soc_after)}%.
        </p>
      )}
    </section>
  );
}

function TripBody({ t }: { t: SampleTrip }) {
  const tot = t.totals;
  return (
    <Frame>
      <article data-sample-trip={t.slug} className="space-y-6">
        <div className="space-y-3">
          <h1 className="text-3xl font-semibold tracking-tight text-neutral-50">{t.title}</h1>
          <p className="text-neutral-300">{t.intro}</p>
          <p className="text-sm text-neutral-400">A simulated sample plan: every figure is an estimate for a sample truck.</p>
        </div>
        <dl className="grid grid-cols-2 gap-2 text-sm sm:grid-cols-4">
          <div className={stat}><dt className="text-xs text-neutral-400">Distance</dt><dd className="text-neutral-100">{Math.round(tot.distance_mi)} mi</dd></div>
          <div className={stat}><dt className="text-xs text-neutral-400">Driving</dt><dd className="text-neutral-100">{fmtDuration(tot.drive_min)}</dd></div>
          <div className={stat}><dt className="text-xs text-neutral-400">Charging</dt><dd className="text-neutral-100">{fmtDuration(tot.charge_min)} · {tot.charge_stops} {tot.charge_stops === 1 ? "stop" : "stops"}</dd></div>
          <div className={stat}>
            <dt className="text-xs text-neutral-400">Fast-charge cost</dt>
            <dd className="text-neutral-100">
              {(tot.priced_stops ?? 0) > 0 ? (
                <>about {fmtRange(tot.dcfc_cost_low ?? 0, tot.dcfc_cost_high ?? 0, (v) => formatMoney(v, t.currency))} <span className="text-xs text-neutral-400">
                    (estimate, typical price seen by <TrackerLink url={tot.price_url} />
                    {priceMonth(tot.price_month) ? `, ${priceMonth(tot.price_month)}` : ""}
                    {(tot.unpriced_stops ?? 0) > 0 ? `; ${tot.priced_stops} of ${(tot.priced_stops ?? 0) + (tot.unpriced_stops ?? 0)} stops priced` : ""})
                  </span></>
              ) : (
                <span className="text-sm text-neutral-300">Price shown at the charger</span>
              )}
            </dd>
          </div>
        </dl>
        <RouteMap route={t.route} title={t.title} stops={t.days.flatMap((d) => d.stops)} />
        {t.days.map((d, i) => <Day key={`${d.label}-${i}`} d={d} currency={t.currency} />)}
        <div className="flex flex-wrap gap-3 border-t border-neutral-900 pt-5">
          {safeDemoUrl(t.demo_url) && <a href={safeDemoUrl(t.demo_url) as string} className={btn}>Open in the demo planner</a>}
          <a href={safeSignupUrl(t.signup_url)} className={btnPrimary}>Plan your own</a>
        </div>
      </article>
    </Frame>
  );
}

export default function SampleTripPage() {
  const t = useMemo(readSampleTrip, []);
  return t ? <TripBody t={t} /> : <Missing />;
}

function Card({ c }: { c: SampleTripCard }) {
  return (
    <li className="rounded-lg border border-neutral-800 p-4">
      <h2 className="text-lg font-semibold text-neutral-50">
        <a href={`/sample-trips/${c.slug}`} className="hover:underline">{c.title}</a>
      </h2>
      <p className="mt-1 text-sm text-neutral-400">
        {Math.round(c.totals.distance_mi)} mi · {c.totals.charge_stops} {c.totals.charge_stops === 1 ? "charging stop" : "charging stops"}
      </p>
      {c.blurb && <p className="mt-1 text-sm text-neutral-300">{c.blurb}</p>}
    </li>
  );
}

export function SampleTripsHubPage() {
  const trips = useMemo(readSampleTrips, []);
  if (!trips) return <Missing />;
  return (
    <Frame>
      <div className="space-y-3">
        <h1 className="text-3xl font-semibold tracking-tight text-neutral-50">Sample Rivian road trips, planned with Rivolt</h1>
        <p className="text-neutral-300">
          Simulated sample plans for a Rivian R1S, laid out the way Rivolt shows them: each charging stop, the battery on arrival and departure, charge time and the estimated cost.
          Estimates only; a real trip will differ with weather, traffic and charger speed.
        </p>
      </div>
      <ul className="space-y-3" data-sample-trips>
        {trips.map((c) => <Card key={c.slug} c={c} />)}
      </ul>
      <div className="flex flex-wrap gap-3">
        <a href="/signup" className={btnPrimary}>Plan your own</a>
      </div>
    </Frame>
  );
}
