import { useQuery } from "@tanstack/react-query";
import { fetchMembershipTerms, groupByNetwork, safeSourceUrl } from "../lib/membershipTerms";

// MembershipTerms: what each network says its membership plans cost and include, from the network's own page, each with where it was
// read and when it was last checked. These are not your prices; your own rates are in the list above. Hidden when there are no rows.
export function MembershipTerms() {
  const q = useQuery({ queryKey: ["membership-terms"], queryFn: ({ signal }) => fetchMembershipTerms(signal), staleTime: 60 * 60_000, retry: 0 });
  const groups = groupByNetwork(q.data ?? []);
  if (q.isError || groups.length === 0) return null;
  return (
    <section aria-labelledby="membership-terms-h" data-membership-terms className="mt-4 space-y-3 border-t border-neutral-800 pt-4">
      <div>
        <h3 id="membership-terms-h" className="text-sm font-medium text-neutral-100">Membership plans, as the networks list them</h3>
        <p className="mt-0.5 text-xs text-neutral-500">
          Read from each network&apos;s own page, with the date it was last checked. These are the plans&apos; terms, not your prices; plans change, so check the network before you
          sign up.
        </p>
      </div>
      <ul className="space-y-3">
        {groups.map((g) => (
          <li key={g.slug}>
            <div className="text-sm font-medium text-neutral-200">{g.network}</div>
            <ul className="mt-1 space-y-2">
              {g.plans.map((p) => {
                const href = safeSourceUrl(p.source_url);
                return (
                  <li key={`${p.network_slug}/${p.plan}`} data-membership-row className="rounded-md border border-neutral-800 px-3 py-2">
                    <div className="text-sm text-neutral-100">{p.plan}</div>
                    <div className="text-xs text-neutral-300">{p.terms}</div>
                    <div className="mt-1 text-[11px] text-neutral-500">
                      {p.label}
                      {p.read_via ? ` Read from ${p.read_via === "page" ? "the network's page" : "a search snippet"}.` : ""}
                      {href && (
                        <>
                          {" "}
                          <a href={href} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-300">Source</a>
                        </>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
          </li>
        ))}
      </ul>
    </section>
  );
}
