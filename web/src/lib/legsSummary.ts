// "13 min", "1 h 5 min", "2 h": how long the truck sat between two legs (a whole number of hours drops the "0 min", as on iOS).
export function parkedText(min: number): string {
  if (min < 60) return `${min} min`;
  if (min % 60 === 0) return `${min / 60} h`;
  return `${Math.floor(min / 60)} h ${min % 60} min`;
}

// legsSummary is the collapsed Legs row: "3 legs · parked 13 min, 19 min". Up to three stops are listed; more than that, "parked 4 times".
export function legsSummary(legs: { parkedMinAfter?: number | null }[]): string {
  const parked = legs.map((l) => l.parkedMinAfter).filter((m): m is number => m != null);
  const head = `${legs.length} legs`;
  if (parked.length === 0) return head;
  if (parked.length > 3) return `${head} · parked ${parked.length} times`;
  return `${head} · parked ${parked.map(parkedText).join(", ")}`;
}
