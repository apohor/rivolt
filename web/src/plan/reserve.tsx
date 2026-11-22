import { createContext, useContext } from "react";

// plan_v2: the person's reserve (the "Target arrival SoC" they set, %) for the planner's result. An arrival charge under it is tinted wherever the
// result shows one. null: no reserve to compare with (the key is off, or the field is empty).
const ReserveContext = createContext<number | null>(null);
export const ReserveProvider = ReserveContext.Provider;
export const useReservePct = (): number | null => useContext(ReserveContext);

// reservePct reads the form's field: a number from 1 to 100, else null.
export function reservePct(field: string): number | null {
  const t = field.trim();
  if (t === "") return null;
  const n = Number(t);
  return Number.isFinite(n) && n > 0 && n <= 100 ? n : null;
}

// underReserve: the arrival is known (above 0) and below the reserve.
export const underReserve = (arrival: number, reserve: number | null): boolean => reserve != null && arrival > 0 && arrival < reserve;
