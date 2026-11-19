import { Link } from "react-router-dom";
import { useEffect, useState } from "react";
import Logo from "../components/Logo";
import { getConfig } from "../lib/config";
import { track } from "../lib/track";
import HeroTrip from "./landing/HeroTrip";
import Terrain from "./landing/Terrain";
import PhoneTour from "./landing/PhoneTour";
import PlannerShots from "./landing/PlannerShots";
import { visitSourceProps } from "../lib/visitSource";
import "../styles/landing.css";

// Public main page for signed-out visitors, rendered at "/" only when the
// install enables it (#1909). One job: open the trip planner demo or connect
// a truck. Every figure on it is the demo truck's and is labelled as a
// sample; the planner screenshots are real (radar layer on). No prices, no
// trial or "free" wording, nothing from another host. Title, description,
// canonical and structured data come from the server (internal/api/seo.go).

const REPO = "https://github.com/apohor/rivolt";
// The public sample-truck demo lives on its own host. On the hosted
// instance the page links there; a self-host has no such host and shows
// its in-app /demo only when its operator enabled it.
const DEMO_URL = "https://demo.rivolt.dev";
// The hero's sample trip (Las Vegas weekend, internal/demo/trips): the CTA
// opens that same plan, cost included, in the demo planner.
const SAMPLE_TRIP = "/demo/trips/plan?preset=7041ba4f-947f-50d3-882d-e5f5f9d08b82";

function Arrow() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M3 8h10M9 4l4 4-4 4" />
    </svg>
  );
}

// A link into the demo: in-app on an install with the demo on, else the
// public demo host. demo_click carries only where it sits (hero or footer).
function DemoLink({ path, where, className, children }: { path: string; where: "hero" | "footer"; className?: string; children: React.ReactNode }) {
  const { demoEnabled } = getConfig().features;
  const onClick = () => track("demo_click", { where });
  return demoEnabled ? (
    <Link to={path} onClick={onClick} className={className}>
      {children}
    </Link>
  ) : (
    <a href={`${DEMO_URL}${path}`} onClick={onClick} className={className}>
      {children}
    </a>
  );
}

function Tile({ icon, title, children }: { icon: React.ReactNode; title: React.ReactNode; children: React.ReactNode }) {
  return (
    <article className="lp-tile rv">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        {icon}
      </svg>
      <div>
        <h3>{title}</h3>
        <p>{children}</p>
      </div>
    </article>
  );
}

function Claim({ title, body, children }: { title: React.ReactNode; body: string; children?: React.ReactNode }) {
  return (
    <article className="lp-tile rv">
      <div>
        <h3>{title}</h3>
        <p>{body}</p>
        {children}
      </div>
    </article>
  );
}

export default function LandingPage() {
  const { demoEnabled, signupEnabled } = getConfig().features;
  const hosted = /(^|\.)rivolt\.dev$/.test(window.location.hostname);
  const demo = demoEnabled || hosted;
  const connect = signupEnabled ? { to: "/signup", label: "Connect your Rivian" } : { to: "/login", label: "Sign in" };
  const [stuck, setStuck] = useState(false);
  useEffect(() => {
    // Referrer host and utm_source only (tokens, never a full URL).
    track("landing_view", visitSourceProps(window.location.search, document.referrer));
  }, []);
  useEffect(() => {
    const onScroll = () => setStuck(scrollY > 8);
    addEventListener("scroll", onScroll, { passive: true });
    return () => removeEventListener("scroll", onScroll);
  }, []);

  const ctas = (where: "hero" | "footer") => (
    <div className="cta">
      {demo && (
        <DemoLink path={SAMPLE_TRIP} where={where} className="btn go">
          Plan a sample trip
          <Arrow />
        </DemoLink>
      )}
      <Link to={connect.to} className={demo ? "btn" : "btn go"}>
        {connect.label}
      </Link>
    </div>
  );

  return (
    <div className="lp">
      <header className="lp-nav" data-stuck={String(stuck)}>
        <div className="wrap">
          <span className="lp-logo">
            <Logo size={24} />
            Rivolt
          </span>
          <nav aria-label="Sections">
            <a href="#planner">Trip planner</a>
            <a href="#tour">Your truck</a>
          </nav>
          <Link to="/login" className="signin">
            Sign in
          </Link>
          {demo && (
            <DemoLink path="/demo" where="hero" className="btn go sm">
              Try the demo
            </DemoLink>
          )}
        </div>
      </header>

      <main>
        <div className="lp-hero">
          <Terrain />
          <div className="wrap lp-hero-grid">
            <div>
              <p className="tag">
                <b>Trip planner for Rivian owners</b>
              </p>
              <h1>
                <span className="w">Plan</span> <span className="w" style={{ animationDelay: ".07s" }}>the</span>{" "}
                <span className="w" style={{ animationDelay: ".14s" }}>trip.</span>{" "}
                <br />
                <span className="w" style={{ animationDelay: ".25s" }}>
                  <em>Charging</em>
                </span>{" "}
                <span className="w" style={{ animationDelay: ".33s" }}>
                  <em>included.</em>
                </span>
              </h1>
              <p className="lede">
                Where to stop, how long to charge, what you arrive with and what it costs. And how weather and elevation will affect your trip.
              </p>
              {ctas("hero")}
              {demo && <p className="fine">The demo uses a sample truck and needs no account.</p>}
            </div>
            <HeroTrip />
            <p className="fine">
              {demo ? "The demo uses a generated sample truck: no sign-in, no Rivian account. " : ""}
              Free to start; Rivolt Pro is $3.99 a month or $34.99 a year. Sign up with Google or GitHub; no invite needed while there is room.
            </p>
          </div>
        </div>

        <section className="lp-planner" id="planner" aria-labelledby="lp-planner-h">
          <div className="wrap">
            <div className="head rv">
              <p className="tag">
                <b>Trip planner</b>
              </p>
              <h2 id="lp-planner-h">
                The route, the stops, the <em className="amber">bill.</em>
              </h2>
            </div>
            <PlannerShots />
            {/* The shots show our map, so they carry its credits (the words of the map's own attribution, BASEMAP_ATTRIBUTION) and the radar's source. */}
            <p className="lp-shot-credit">
              Map:{" "}
              <a href="https://protomaps.com" target="_blank" rel="noopener">
                Protomaps
              </a>{" "}
              ©{" "}
              <a href="https://openstreetmap.org/copyright" target="_blank" rel="noopener">
                OpenStreetMap
              </a>{" "}
              contributors · Radar: NOAA/NWS
            </p>
            <div className="lp-bento">
              <Tile title="Stops chosen for you" icon={<path d="M13 3 5 13h6l-1 8 8-10h-6z" />}>
                Charger, minutes plugged in and the charge you arrive with.
              </Tile>
              <Tile title="Weather and elevation" icon={<path d="M7 17a4.5 4.5 0 0 1 .6-8.96A6 6 0 0 1 19 9.5 3.8 3.8 0 0 1 18 17zM9 20v1M13 20v1M17 20v1" />}>
                See how weather and climbs will affect your trip, with a live radar layer on the map.
              </Tile>
              <Tile title="What is on the way" icon={<path d="M3 20h18M5 20V9l7-5 7 5v11M10 20v-5h4v5" />}>
                Chargers, hotels and campgrounds, plus your own stops.
              </Tile>
              <Tile title="Multi-day trips" icon={<path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" />}>
                Overnight stays, and plan vs actual when you are back.
              </Tile>
            </div>
          </div>
        </section>

        <section id="tour" aria-labelledby="lp-tour-h">
          <div className="wrap">
            <div className="head rv">
              <p className="tag">
                <b>Between trips</b>
              </p>
              <h2 id="lp-tour-h">
                And every mile <em>in between.</em>
              </h2>
            </div>
            <PhoneTour />
          </div>
        </section>

        <section id="pricing" aria-labelledby="pricing-heading">
          <div className="wrap">
          <div className="head rv">
            <p className="tag">
              <b>Pricing</b>
            </p>
            <h2 id="pricing-heading">
              Free and <em>Pro</em>
            </h2>
          </div>
          <div className="grid gap-6 md:grid-cols-2">
            <Claim
              title="Free"
              body="Every drive and charge recorded and kept until you delete it. Now, Activity, Money and Truck for the last 12 months, the monthly statement with a CSV for each month it shows, a single-day trip planner, three saved trips, trip share links, all share cards, the small Now widget and the charging Live Activity, imports, every alert, and the full export of everything, always. One vehicle shown at a time."
            />
            <Claim
              title="Rivolt Pro: $3.99 a month or $34.99 a year"
              body="Unlimited history and trends, range near a temperature, multi-day plans with hotel and campground overnights, re-plan from where the truck is, plan-vs-actual, unlimited saved trips, every vehicle, and every iPhone widget except the small Now widget. Stop paying and nothing is deleted."
            >
              <Link to="/support" className="mt-3 inline-block text-sm text-emerald-300 hover:underline">
                What is free, what Pro costs, what happens if you stop
              </Link>
            </Claim>
          </div>
          <p className="fine">
            Subscribe on the web; iPhone purchase coming soon. Web payments through Stripe; Rivolt never sees your card. Web purchases need a US billing address for now. Self-hosting is complete and free. Unofficial, not affiliated with Rivian.
          </p>
          </div>
        </section>

        <section style={{ paddingTop: 0 }} aria-labelledby="lp-next-h">
          <div className="wrap">
            <div className="head rv">
              <p className="tag">
                <b>Coming next</b>
              </p>
              <h2 id="lp-next-h">
                The <em>iPhone</em> app.
              </h2>
            </div>
            <div className="lp-bento" style={{ maxWidth: 860 }}>
              <Tile
                title={
                  <>
                    iPhone app <span className="lp-soon">Coming next</span>
                  </>
                }
                icon={
                  <>
                    <rect x="7" y="2.5" width="10" height="19" rx="2.5" />
                    <path d="M11 18.5h2" />
                  </>
                }
              >
                The trip planner and your truck, in your pocket.
              </Tile>
            </div>
          </div>
        </section>

        <section style={{ paddingTop: 0 }} aria-labelledby="lp-close-h">
          <div className="wrap">
            <div className="lp-close-box rv">
              <svg className="road" viewBox="0 0 520 300" fill="none" aria-hidden="true">
                <path
                  d="M92 238 C134 244 164 244 172 228 S190 178 204 159 S256 140 274 130 S312 104 328 98 S372 92 382 80 S404 36 410 18"
                  stroke="#5ee9b5"
                  strokeWidth="5"
                  strokeLinecap="round"
                  pathLength={1}
                />
              </svg>
              <h2 id="lp-close-h">
                Where to <em>next?</em>
              </h2>
              {ctas("footer")}
            </div>
          </div>
        </section>
      </main>

      <footer className="lp-footer">
        <div className="wrap">
          <nav aria-label="More from Rivolt">
            <a href={REPO}>GitHub</a>
            <Link to="/privacy">Privacy</Link>
            <Link to="/terms">Terms</Link>
            <Link to="/support">Support</Link>
            <Link to="/safe-to-connect">Is it safe to connect?</Link>
            <Link to="/moving-from-electrafi">Moving from ElectraFi</Link>
            <Link to="/lease-calculator">Lease calculator</Link>
            {demo && (
              <DemoLink path="/demo" where="footer">
                Demo
              </DemoLink>
            )}
          </nav>
          <p>
            Rivian is a trademark of Rivian Automotive, LLC. Rivolt is an independent, open-source project and is not affiliated with, endorsed by
            or sponsored by Rivian.
          </p>
          {hosted && <p>Operated by ZeroDowntime LLC · support@rivolt.dev</p>}
        </div>
      </footer>
    </div>
  );
}
