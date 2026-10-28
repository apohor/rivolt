// The public /charging-networks page's data (GET /api/charging-networks, no sign-in): each network's membership plans as the operator states
// them with the date checked, and where a third-party tracker publishes a typical observed price, that price with its month. The server drops
// anything older than twelve months, so a row may have no plans or no price. Sourced facts only: nothing here is a price for the reader.
import { api } from "./api";
import { safeSourceUrl } from "./membershipTerms";

export type NetworkPlan = { plan: string; terms: string; source_url?: string; checked?: string; label: string; read_via?: string };
export type NetworkObserved = { low_per_kwh: number; high_per_kwh: number; low_note?: string; high_note?: string; month: string; source: string; url: string; label: string };
export type NetworkRow = { slug: string; name: string; plans: NetworkPlan[] | null; observed: NetworkObserved | null; price: string };
export type ChargingNetworks = { networks: NetworkRow[]; observed_terms_url?: string; observed_source?: string };

export const PRICE_AT_CHARGER = "Price shown at the charger.";
// DCFC Tracker's terms page: the page links it under Table 2. Without that link, Table 2 shows no figure at all (DA's condition).
export const OBSERVED_TERMS_HOST = "dcfctracker.com";

export const fetchChargingNetworks = async (signal?: AbortSignal): Promise<ChargingNetworks | null> => {
  const r = await api.get<ChargingNetworks | null>("/api/charging-networks", signal);
  return r && Array.isArray(r.networks) ? r : null;
};

// A link to the tracker is shown only when it is an https page on the tracker's own site.
export function safeTrackerUrl(u: string | undefined | null): string | null {
  if (!u) return null;
  try {
    const url = new URL(u);
    const host = url.hostname.toLowerCase();
    return url.protocol === "https:" && !url.username && !url.password && (host === OBSERVED_TERMS_HOST || host.endsWith(`.${OBSERVED_TERMS_HOST}`)) ? u : null;
  } catch {
    return null;
  }
}

// What a Table 2 cell says: the server's ready line with a link to the report it came from, but only while the tracker's terms are linked
// under the table; otherwise "Price shown at the charger." for every row.
export function priceCell(n: NetworkRow, termsLinked: boolean): { text: string; href: string | null } {
  if (!termsLinked || !n.observed) return { text: PRICE_AT_CHARGER, href: null };
  const text = n.price && n.price !== PRICE_AT_CHARGER ? n.price : n.observed.label || PRICE_AT_CHARGER;
  return { text, href: safeTrackerUrl(n.observed.url) };
}

export const planSourceHref = (p: NetworkPlan): string | null => safeSourceUrl(p.source_url);
export const hasPlans = (n: NetworkRow): boolean => (n.plans?.length ?? 0) > 0;
