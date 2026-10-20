import type { ChargingNetwork } from "./api";

// A network row's rate is "unset" until the user enters one: the seeded mid-2025 rates count as unset everywhere, so the
// server sends them blank (0). Nothing prices a charge from an unset rate, and a one-click apply waits for the user's own.
export const networkRateSet = (n: Pick<ChargingNetwork, "price_per_kwh" | "member_price_per_kwh">): boolean =>
  (n.price_per_kwh ?? 0) > 0 || (n.member_price_per_kwh ?? 0) > 0;

export const SET_RATE_FIRST = "Set your rate in Settings first";
export const SET_RATE_PATH = "/settings?tab=charging#networks";
export const UNSET_RATE_NOTE = "Enter the rate you pay. Prices vary by site.";

// The saving between guest and member prices, only when the user entered both.
export const memberSaving = (n: Pick<ChargingNetwork, "price_per_kwh" | "member_price_per_kwh">): number =>
  (n.price_per_kwh ?? 0) > 0 && (n.member_price_per_kwh ?? 0) > 0 ? Math.max(0, n.price_per_kwh - (n.member_price_per_kwh ?? 0)) : 0;

// slug -> display name from the user's networks list (an unpriced session carries only the slug).
export const networkNameFromSlug = (networks: Pick<ChargingNetwork, "slug" | "name">[] | undefined, slug: string | null | undefined): string | null =>
  (slug && networks?.find((n) => n.slug === slug)?.name) || null;
