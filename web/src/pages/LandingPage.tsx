import { Link } from "react-router-dom";
import Logo from "../components/Logo";
import { useEffect } from "react";
import { getConfig } from "../lib/config";
import { track } from "../lib/track";

// Public landing page for signed-out visitors, rendered at "/" only when
// the install enables it. Copy makes claims about Rivolt's own behaviour only,
// nothing about Rivian's app, no "real-time" or "battery-friendly"
// headline. Illustrations are drawn components with masked figures,
// never real data or screenshots. No third-party assets.

const REPO = "https://github.com/apohor/rivolt";
// The public sample-truck demo lives on its own host. On the hosted
// instance the landing links there; a self-host has no such host and
// shows the in-app /demo link only when its operator enabled it.
const DEMO_URL = "https://demo.rivolt.dev";

export default function LandingPage() {
  const { demoEnabled, signupEnabled } = getConfig().features;
  const hostedDemo = !demoEnabled && /(^|\.)rivolt\.dev$/.test(window.location.hostname);
  const demo = demoEnabled || hostedDemo;
  useEffect(() => {
    // Referrer host and utm_source only (tokens, never a full URL).
    const tok = (v: string | null) => (v && /^[a-z0-9_.-]{1,40}$/.test(v.toLowerCase()) ? v.toLowerCase() : undefined);
    let host: string | undefined;
    try {
      host = document.referrer ? tok(new URL(document.referrer).hostname) : undefined;
    } catch {
      host = undefined;
    }
    const props: Record<string, string> = {};
    if (host) props.referrer_host = host;
    const utm = tok(new URLSearchParams(window.location.search).get("utm_source"));
    if (utm) props.utm_source = utm;
    track("landing_view", props);
  }, []);
  return (
    <div className="min-h-full bg-neutral-950 text-neutral-100">
      <header className="mx-auto flex max-w-5xl items-center justify-between px-4 py-4">
        <span className="flex items-center gap-2 font-semibold tracking-tight">
          <Logo size={22} className="text-emerald-400" />
          Rivolt
        </span>
        <Link to="/login" className="text-sm text-neutral-300 hover:text-neutral-100">
          Sign in
        </Link>
      </header>

      <main className="mx-auto max-w-5xl space-y-16 px-4 pb-16">
        <section className="grid gap-8 pt-6 md:grid-cols-2 md:items-center">
          <div>
            <h1 className="text-4xl font-semibold tracking-tight md:text-5xl">What your Rivian actually costs.</h1>
            <p className="mt-4 text-lg text-neutral-300">
              Every charge and trip priced at your rates, or flagged when we can&apos;t price it, with the source of
              every number. Open source. Self-host it or let us run it.
            </p>
            <div className="mt-6 flex flex-wrap gap-3">
              {demoEnabled && (
                <Link
                  to="/demo"
                  onClick={() => track("demo_click", { where: "hero" })}
                  className="rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-500"
                >
                  Try the demo truck
                </Link>
              )}
              {hostedDemo && (
                <a
                  href={DEMO_URL}
                  onClick={() => track("demo_click", { where: "hero" })}
                  className="rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-500"
                >
                  Try the demo truck
                </a>
              )}
              <Link
                to={signupEnabled ? "/signup" : "/login"}
                className={
                  demo
                    ? "rounded-md border border-neutral-700 px-4 py-2 text-sm font-semibold text-neutral-100 hover:bg-neutral-900"
                    : "rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-500"
                }
              >
                {signupEnabled ? "Create an account" : "Sign in"}
              </Link>
            </div>
            <p className="mt-3 text-xs text-neutral-500">
              {demo ? "The demo uses a generated sample truck: no sign-in, no Rivian account. " : ""}
              Free while in beta. Sign up with Google or GitHub; no invite needed while there is room.
            </p>
          </div>
          <StatementIllustration />
        </section>

        <section aria-label="What Rivolt does" className="grid gap-6 md:grid-cols-3">
          <Claim
            title="Every dollar has a source"
            body="Billed by Rivian, estimated from your home rate, estimated from the charging network, or entered by you. Charges we can't price are flagged, not guessed. You always see which one."
          >
            <SourceLegend />
          </Claim>
          <Claim
            title="Price the trip before you go"
            body="See fast-charging spend versus charging at home before you leave, so the fast-charging stops on a road trip are no surprise."
          >
            {/* A plain link: the sample-trip pages carry their data in the server-rendered HTML, which only a full page load fetches. */}
            <a href="/sample-trips/la-las-vegas-two-days" className="mt-3 inline-block text-sm text-emerald-300 hover:underline">
              See a planned trip first: Los Angeles to Las Vegas in two days
            </a>
          </Claim>
          <Claim
            title="Yours to keep"
            body="MIT licensed. Export your data as a zip of CSVs, or download a full JSON backup. Run it on your own server with docker compose and an OIDC provider (maps and the planner need the optional tiles and Valhalla services), or use ours."
          >
            <a href={REPO} className="mt-3 inline-block text-sm text-emerald-300 hover:underline">
              Source and self-host guide
            </a>
          </Claim>
        </section>

        <section aria-label="More of what Rivolt does" className="grid gap-6 md:grid-cols-3">
          <Claim
            title="Lease miles, projected"
            body="Enter your allowance and term. Rivolt projects your miles at lease return and what the overage would cost at your per-mile rate."
          >
            <Link to="/lease-calculator" className="mt-3 inline-block text-sm text-emerald-300 hover:underline">
              Try the free lease calculator
            </Link>
          </Claim>
          <Claim
            title="Efficiency, explained"
            body="mi/kWh broken down by outside temperature, speed and drive mode, so you can see what actually moves your number."
          />
          <Claim
            title="Software update alerts"
            body="A push notification when an update is offered or installs, and release notes linked from the Truck tab."
          />
        </section>

        <section aria-label="Coming from ElectraFi" className="rounded-xl border border-neutral-800 p-6">
          <h2 className="text-xl font-semibold">Coming from ElectraFi?</h2>
          <p className="mt-3 text-sm text-neutral-300">
            Bring your history with you. Download your data from ElectraFi, then upload the CSVs under Settings, Data,
            Import ElectraFi CSV. Rivolt skips anything it has already recorded.
          </p>
          <Link to="/moving-from-electrafi" className="mt-3 inline-block text-sm text-emerald-300 hover:underline">
            Import steps
          </Link>
        </section>

        <section aria-label="Your Rivian login" className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-6">
          <h2 className="text-xl font-semibold">How your Rivian login is handled</h2>
          <ul className="mt-3 space-y-2 text-sm text-neutral-300">
            <li>Your Rivian session is stored encrypted, and your Rivian password is never stored.</li>
            <li>
              We recommend connecting a separate Authorized Driver account, so Rivolt never holds your primary
              login.
            </li>
            <li>
              With an Authorized Driver account you can revoke Rivolt from the Rivian app at any time. Either way you
              can delete your data from Settings.
            </li>
          </ul>
          <Link to="/safe-to-connect" className="mt-3 inline-block text-sm text-emerald-300 hover:underline">
            Is it safe to connect my Rivian account?
          </Link>
        </section>

        <section aria-label="Limits" className="rounded-xl border border-neutral-800 p-6">
          <h2 className="text-xl font-semibold">What Rivolt can&apos;t do</h2>
          <ul className="mt-3 space-y-2 text-sm text-neutral-300">
            <li>
              Unofficial: Rivolt uses Rivian&apos;s unofficial API and is not affiliated with Rivian. It can break.
            </li>
            <li>No fleet leaderboards or software-rollout tracking.</li>
            <li>Beta: expect rough edges.</li>
          </ul>
        </section>
      </main>

      <footer className="border-t border-neutral-800 px-4 py-6 text-xs text-neutral-500">
        <div className="mx-auto max-w-5xl space-y-1">
          <p>
            Rivolt is open source under the MIT license.{" "}
            <a href={REPO} className="text-neutral-300 underline underline-offset-2 hover:text-neutral-100">
              GitHub
            </a>
            {" · "}
            <Link to="/privacy" className="text-neutral-300 underline underline-offset-2 hover:text-neutral-100">
              Privacy
            </Link>
            {" · "}
            <Link to="/terms" className="text-neutral-300 underline underline-offset-2 hover:text-neutral-100">
              Terms
            </Link>
            {" · "}
            <Link to="/support" className="text-neutral-300 underline underline-offset-2 hover:text-neutral-100">
              Support
            </Link>
            {" · "}
            <Link to="/lease-calculator" className="text-neutral-300 underline underline-offset-2 hover:text-neutral-100">
              Lease calculator
            </Link>
            {demoEnabled && (
              <>
                {" · "}
                <Link
                  to="/demo"
                  onClick={() => track("demo_click", { where: "footer" })}
                  className="text-neutral-300 underline underline-offset-2 hover:text-neutral-100"
                >
                  Demo
                </Link>
              </>
            )}
            {hostedDemo && (
              <>
                {" · "}
                <a
                  href={DEMO_URL}
                  onClick={() => track("demo_click", { where: "footer" })}
                  className="text-neutral-300 underline underline-offset-2 hover:text-neutral-100"
                >
                  Demo
                </a>
              </>
            )}
          </p>
          <p>
            Rivian is a trademark of Rivian Automotive, LLC. Rivolt is an independent project and is not affiliated
            with, endorsed by or sponsored by Rivian.
          </p>
        </div>
      </footer>
    </div>
  );
}

function Claim({ title, body, children }: { title: string; body: string; children?: React.ReactNode }) {
  return (
    <div>
      <h2 className="text-lg font-semibold">{title}</h2>
      <p className="mt-2 text-sm text-neutral-300">{body}</p>
      {children}
    </div>
  );
}

// Drawn illustration of a statement with masked figures: it shows the
// shape of the product (source labels per charge), not anyone's data.
function StatementIllustration() {
  const rows: { label: string; source: string; tone: string }[] = [
    { label: "Home charge", source: "home rate", tone: "text-emerald-300" },
    { label: "Fast charge", source: "network estimate", tone: "text-amber-300" },
    { label: "Fast charge", source: "billed by Rivian", tone: "text-sky-300" },
    { label: "Away charge", source: "needs a price", tone: "text-neutral-400" },
  ];
  return (
    <div
      role="img"
      aria-label="Illustration of a monthly statement: each charge lists its price source. Amounts are masked."
      className="rounded-xl border border-neutral-800 bg-neutral-900/60 p-5"
    >
      <div className="text-xs uppercase tracking-wide text-neutral-500">Sample statement · figures masked</div>
      <div className="mt-1 text-4xl font-semibold tabular-nums">$•••.••</div>
      <ul className="mt-4 divide-y divide-neutral-800 text-sm">
        {rows.map((r, i) => (
          <li key={i} className="flex items-baseline justify-between gap-3 py-2">
            <span>{r.label}</span>
            <span className="flex items-baseline gap-3">
              <span className={`text-xs ${r.tone}`}>{r.source}</span>
              <span className="tabular-nums text-neutral-400">$•.••</span>
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function SourceLegend() {
  const items = [
    ["Billed", "bg-sky-400"],
    ["Home rate", "bg-emerald-400"],
    ["Network estimate", "bg-amber-400"],
    ["Entered by you", "bg-neutral-400"],
  ] as const;
  return (
    <ul className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-neutral-400">
      {items.map(([t, c]) => (
        <li key={t} className="flex items-center gap-1.5">
          <span className={`inline-block h-2 w-2 rounded-full ${c}`} />
          {t}
        </li>
      ))}
    </ul>
  );
}
