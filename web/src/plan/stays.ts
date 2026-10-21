// Campgrounds from Recreation.gov (RIDB): what to say about one, and the only links we draw from the data.
import type { HookupId } from "./hookups";
import type { POI } from "../lib/poi";

export const CAMP_WARNING = "Reservation required. Listed, not guaranteed. Some parks don't allow EV charging; ask first.";

// The credit CC BY asks for: who, the licence, and that Rivolt filtered the data. Fixed text and links, never from a response.
export const RIDB_CREDIT = {
  lead: "Data: ",
  source: { label: "Recreation.gov (RIDB)", href: "https://catalog.data.gov/dataset/recreation-information-database-ridb" },
  licence: { label: "CC BY 4.0", href: "https://creativecommons.org/licenses/by/4.0/" },
  tail: "; filtered by Rivolt",
} as const;
export const RIDB_CREDIT_TEXT = `${RIDB_CREDIT.lead}${RIDB_CREDIT.source.label}, ${RIDB_CREDIT.licence.label}${RIDB_CREDIT.tail}`;

// A link to the campground's page on Recreation.gov: only the page, never the reservation flow, and only for a numeric id.
const CAMP_PAGE = /^https:\/\/www\.recreation\.gov\/camping\/campgrounds\/(\d+)$/;
export function campgroundPageUrl(sourceUrl: string | undefined, ridbId?: string): string {
  if (sourceUrl) {
    const m = CAMP_PAGE.exec(sourceUrl);
    return m ? `https://www.recreation.gov/camping/campgrounds/${m[1]}` : "";
  }
  const id = /^(?:ridb\/)?(\d+)$/.exec(ridbId ?? "")?.[1];
  return id ? `https://www.recreation.gov/camping/campgrounds/${id}` : "";
}

export type CampConnector = "j1772" | "nema_14_50" | "nema_tt_30";

// The planner's hookup for a campground outlet.
export const hookupForConnector = (c: CampConnector | undefined): HookupId => (c === "nema_14_50" ? "nema_14_50" : c === "nema_tt_30" ? "tt_30" : "j1772");

export function outletLabel(h: HookupId): string {
  if (h === "nema_14_50") return "50 A hookup (NEMA 14-50)";
  if (h === "tt_30") return "30 A hookup (TT-30)";
  if (h === "outlet_120") return "120 V outlet";
  return "J1772 station";
}

// The power line on a campground card; always an estimate, always the Portable Charger by name.
export function campPowerLine(h: HookupId, kw: number): string {
  if (h === "nema_14_50") return `About ${kw} kW (estimate) on a 50 A site with Rivian's Portable Charger.`;
  if (h === "tt_30") return `30 A, 120 V: needs an adapter not included with the truck; slow (about ${kw} kW, estimate).`;
  return `The station's power varies; planned at ${kw} kW (estimate).`;
}

export const sitesLine = (electric?: number, sites?: number): string =>
  electric && sites && sites >= electric ? `${electric} of ${sites} sites have power` : "";

// From a map pin (amps only): the hookup and planned kW, or null for 15-20 A sites, which are not offered.
export function hookupFromAmps(amps: number | undefined): { hookup: HookupId; kw: number } | null {
  if (!amps) return null;
  if (amps >= 50) return { hookup: "nema_14_50", kw: 7.2 };
  if (amps >= 30) return { hookup: "tt_30", kw: 1.4 };
  return null;
}

// A map pin that is a campground with a hookup we offer, or null.
export function campgroundOfPoi(poi: POI): { hookup: HookupId; kw: number; url: string } | null {
  const ft = (poi.facilityType ?? "").toUpperCase();
  if (ft !== "CAMPGROUND" && ft !== "RV_PARK") return null;
  const h = hookupFromAmps(poi.hookupAmps);
  return h ? { ...h, url: campgroundPageUrl(undefined, poi.ridbID) } : null;
}
