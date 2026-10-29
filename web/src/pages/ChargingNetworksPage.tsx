import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import Logo from "../components/Logo";
import { fetchChargingNetworks, hasPlans, planSourceHref, priceCell, safeTrackerUrl } from "../lib/chargingNetworks";
import { useSystemTheme } from "../lib/theme";

// /charging-networks: a public, signed-out page (growth's copy, DA's labels). Plans as each operator states them, with the date checked, and
// typical observed prices where a tracker publishes them. Nothing on it is a price for the reader and nothing comes from any account.
const th = "px-2 py-1.5 text-left text-xs font-medium text-neutral-400";
const td = "px-2 py-2 align-top text-sm text-neutral-200";

export default function ChargingNetworksPage() {
  useSystemTheme(true);
  const q = useQuery({ queryKey: ["charging-networks", "public"], queryFn: ({ signal }) => fetchChargingNetworks(signal), staleTime: 60 * 60_000, retry: 0 });
  const nets = q.data?.networks ?? [];
  const termsHref = safeTrackerUrl(q.data?.observed_terms_url);
  const withPlans = nets.filter(hasPlans);

  return (
    <div className="min-h-full bg-neutral-950 text-neutral-200">
      <header className="border-b border-neutral-900">
        <div className="mx-auto flex max-w-3xl items-center justify-between gap-3 px-4 py-3">
          <Link to="/" aria-label="Rivolt home" className="inline-flex items-center gap-2 text-sm font-semibold text-neutral-100">
            <Logo />
          </Link>
          <nav aria-label="Site" className="flex items-center gap-4 text-sm">
            <a href="/sample-trips" className="text-neutral-400 hover:text-neutral-100">Sample trips</a>
            <Link to="/" className="text-neutral-400 hover:text-neutral-100">Back to Rivolt</Link>
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-3xl space-y-8 px-4 py-8 sm:py-12" data-charging-networks>
        <div>
          <h1 className="text-3xl font-semibold tracking-tight text-neutral-50">Fast-charging networks: plans and typical prices</h1>
          <p className="mt-2 text-base leading-relaxed text-neutral-400">
            No network publishes one national price per kWh; the price is per site and shown in the app or at the charger. What they do publish is their plan
            structure. This page lists that, as each operator states it, with the date we last checked, and where a third-party tracker publishes a typical
            observed price we show it with its month. The price at the charger may differ.
          </p>
        </div>

        {q.isLoading && <p className="text-sm text-neutral-500">Loading…</p>}
        {(q.isError || (q.isSuccess && nets.length === 0)) && <p className="text-sm text-neutral-400">The table is not available right now. Please try again later.</p>}

        {withPlans.length > 0 && (
          <section aria-labelledby="cn-plans" className="space-y-2">
            <h2 id="cn-plans" className="text-xl font-semibold text-neutral-100">Plans, as the networks state them</h2>
            <div className="overflow-x-auto rounded-lg border border-neutral-800">
              <table className="w-full min-w-[34rem]" data-networks-plans>
                <thead className="border-b border-neutral-800 bg-neutral-900/40">
                  <tr><th className={th}>Network</th><th className={th}>Plan</th><th className={th}>As stated</th></tr>
                </thead>
                <tbody className="divide-y divide-neutral-900">
                  {withPlans.flatMap((n) =>
                    (n.plans ?? []).map((p) => {
                      const href = planSourceHref(p);
                      return (
                        <tr key={`${n.slug}/${p.plan}`} data-plan-row>
                          <td className={td}>{n.name}</td>
                          <td className={td}>{p.plan}</td>
                          <td className={td}>
                            <div>{p.terms}</div>
                            <div className="mt-1 text-xs text-neutral-500">
                              {p.label}
                              {href && (
                                <>
                                  {" "}
                                  <a href={href} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-300">Source</a>
                                </>
                              )}
                            </div>
                          </td>
                        </tr>
                      );
                    }),
                  )}
                </tbody>
              </table>
            </div>
          </section>
        )}

        {nets.length > 0 && (
          <section aria-labelledby="cn-prices" className="space-y-2">
            <h2 id="cn-prices" className="text-xl font-semibold text-neutral-100">Typical prices seen</h2>
            <div className="overflow-x-auto rounded-lg border border-neutral-800">
              <table className="w-full min-w-[30rem]" data-networks-prices>
                <thead className="border-b border-neutral-800 bg-neutral-900/40">
                  <tr><th className={th}>Network</th><th className={th}>Typical price</th></tr>
                </thead>
                <tbody className="divide-y divide-neutral-900">
                  {nets.map((n) => {
                    const c = priceCell(n, termsHref !== null);
                    return (
                      <tr key={n.slug} data-price-row>
                        <td className={td}>{n.name}</td>
                        <td className={td}>
                          {c.text}
                          {c.href && (
                            <>
                              {" "}
                              <a href={c.href} target="_blank" rel="noopener noreferrer" className="text-xs underline underline-offset-2 text-neutral-400 hover:text-neutral-200">Source</a>
                            </>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            {termsHref && (
              <p className="text-xs text-neutral-500">
                Prices from <a href={termsHref} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-300">DCFC Tracker (terms)</a>; each shows the month it was seen.
              </p>
            )}
          </section>
        )}

        <section aria-labelledby="cn-how" className="space-y-2">
          <h2 id="cn-how" className="text-xl font-semibold text-neutral-100">How Rivolt uses this</h2>
          <p className="text-sm leading-relaxed text-neutral-300">
            In the trip planner, a stop&apos;s cost is your own member rate where you set one, else the site&apos;s listed price where NREL has one, else the typical
            observed price with the label above, else &ldquo;price shown at the charger&rdquo;. Your recorded charges are never priced from this page; they use what
            you were billed or your own rate.
          </p>
        </section>

        <p className="text-xs leading-relaxed text-neutral-500">
          Unofficial, not affiliated with Rivian or with any charging network. Network names are used descriptively.{" "}
          <a href="/sample-trips" className="underline underline-offset-2 hover:text-neutral-300">See sample trips planned with these prices</a>.
        </p>
      </main>
    </div>
  );
}
