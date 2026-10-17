// Bringing a stop card fully into view when it expands. A charging stop opens
// to a tall detail block; in the phone sheet the itinerary scrolls inside the
// sheet body, so an expanded card near the bottom would otherwise grow below
// the fold (or, scrolled by a helper that targets the detail, leave its title
// above it).

// revealMode: when the card fits in the scroller, move it the least needed
// (block: nearest); when it is taller than the scroller, nearest could keep the
// bottom and cut the title, so the title is aligned to the top instead.
export function revealMode(cardHeight: number, scrollerHeight: number): "card-nearest" | "title-top" {
  return cardHeight > scrollerHeight ? "title-top" : "card-nearest";
}

function reducedMotion(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

// revealStop scrolls an expanded stop card into view. The sheet body is the
// scroller when there is one; otherwise the page is (scroll margins in
// next-plan.css keep the card clear of the sticky bars).
export function revealStop(card: HTMLElement, title: HTMLElement): void {
  const behavior: ScrollBehavior = reducedMotion() ? "auto" : "smooth";
  const scroller = card.closest<HTMLElement>("[data-plan-sheet-body]");
  const mode = scroller ? revealMode(card.getBoundingClientRect().height, scroller.clientHeight) : "card-nearest";
  if (mode === "title-top") title.scrollIntoView?.({ block: "start", behavior });
  else card.scrollIntoView?.({ block: "nearest", behavior });
}
