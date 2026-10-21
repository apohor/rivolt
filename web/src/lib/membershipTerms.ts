// Membership terms for the public charging networks, as listed by each network with a source and the date they were last checked.
// Sourced facts only: nothing here is a price for the user, and a row older than twelve months is not sent by the server.
import { api } from "./api";

export type MembershipTerm = {
  network_slug: string;
  network: string;
  plan: string;
  terms: string;
  source_url: string;
  // "page" (read on the network's page) or "search snippet".
  read_via?: string;
  checked: string;
  // The exact line to show under the row: "As listed by EVgo; may be out of date; last checked 2026-10-20."
  label: string;
};

export const fetchMembershipTerms = async (signal?: AbortSignal): Promise<MembershipTerm[]> => {
  const r = await api.get<{ terms?: MembershipTerm[] | null }>("/api/settings/charging/membership-terms", signal);
  return Array.isArray(r?.terms) ? r.terms : [];
};

// Only the network's own https page is linked; anything else shows as plain text.
export const safeSourceUrl = (u: string | undefined | null): string | null => (u && /^https:\/\/[^\s/]+\.[^\s/]+\//.test(u) ? u : null);

// The terms grouped by network, in the order the server sent them.
export function groupByNetwork(rows: MembershipTerm[]): { slug: string; network: string; plans: MembershipTerm[] }[] {
  const out: { slug: string; network: string; plans: MembershipTerm[] }[] = [];
  for (const r of rows) {
    let g = out.find((x) => x.slug === r.network_slug);
    if (!g) out.push((g = { slug: r.network_slug, network: r.network, plans: [] }));
    g.plans.push(r);
  }
  return out;
}
