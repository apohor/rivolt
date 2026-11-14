import type { HomePlace, StatePrice } from "./api";

// Pure pieces of the first-run setup sheet (home, price, notifications, receipts): what each step says and when it can offer a default.

export const SETUP_DISMISS_KEY = "rivolt.setup.dismissed";

export function setupDismissed(): boolean {
  try {
    return window.localStorage.getItem(SETUP_DISMISS_KEY) === "1";
  } catch {
    return false;
  }
}

export function rememberSetupDismissed(): void {
  try {
    window.localStorage.setItem(SETUP_DISMISS_KEY, "1");
  } catch {
    /* private mode etc.: the sheet comes back next visit */
  }
}

// A usable truck position: finite, in range and not the (0, 0) a truck without a fix reports.
export function truckPosition(s: { latitude?: number; longitude?: number } | null | undefined): { lat: number; lon: number } | null {
  if (!s) return null;
  const { latitude: lat, longitude: lon } = s;
  if (typeof lat !== "number" || typeof lon !== "number" || !Number.isFinite(lat) || !Number.isFinite(lon)) return null;
  if (Math.abs(lat) > 90 || Math.abs(lon) > 180 || (lat === 0 && lon === 0)) return null;
  return { lat, lon };
}

// The place line under the pin: the street or place name, then the state.
export function placeLabel(p: HomePlace | null | undefined): string {
  if (!p) return "";
  return [p.name, p.admin1].filter(Boolean).join(", ");
}

// What the price step may offer: the state average only for a US home the table knows. Anything else is "type your own".
export function priceDefault(place: HomePlace | null | undefined, price: StatePrice | null | undefined): StatePrice | null {
  if (!place || place.country_code.toUpperCase() !== "US" || !price) return null;
  return price;
}

// "$0.33/kWh" for a rate, three decimals only when the rate needs them.
export function rateText(rate: number, currency = "USD"): string {
  const digits = Math.abs(rate * 100 - Math.round(rate * 100)) > 1e-9 ? 3 : 2;
  try {
    return `${new Intl.NumberFormat("en-US", { style: "currency", currency, minimumFractionDigits: digits, maximumFractionDigits: digits }).format(rate)}/kWh`;
  } catch {
    return `${rate.toFixed(digits)} ${currency}/kWh`;
  }
}

// The citation for the default: the table, its period and the date EIA released it.
export function priceCitation(p: StatePrice): string {
  return `${p.source.period} average, residential (EIA, released ${p.source.released})`;
}

// A typed rate: a positive number a household could pay, per kWh. Null for anything else.
export function parseRate(text: string): number | null {
  const n = Number(text.trim().replace(/^[$€£]/, "").replace(",", "."));
  if (!Number.isFinite(n) || n <= 0 || n > 5) return null;
  return n;
}

// The sheet is for an account with something left to set: no home yet, or no home rate. Home and rate both set means it never shows.
export function setupNeeded(home: { set?: boolean } | null | undefined, charging: { home_price_per_kwh?: number } | null | undefined): boolean {
  if (!home || !charging) return false;
  return !home.set || !(Number(charging.home_price_per_kwh) > 0);
}
