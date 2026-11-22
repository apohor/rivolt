import { useLayoutEffect, useSyncExternalStore } from "react";

// Which screens are carrying the Rivian connect card right now. ConnectRivianPrompt marks itself while it shows its connect state; the
// "Rivian isn't connected" banner (layout/DegradedBanner) reads this so that, under signup_v2, it does not repeat the card above it.
// A count, not a flag: two prompts could be mounted at once while a route changes.
let shown = 0;
const subs = new Set<() => void>();
const emit = () => subs.forEach((f) => f());

export function useMarkConnectPromptShown(on: boolean) {
  // A layout effect: the banner must not paint for a frame before the card marks itself, or the card jumps up by the banner's height.
  useLayoutEffect(() => {
    if (!on) return;
    shown++;
    emit();
    return () => {
      shown--;
      emit();
    };
  }, [on]);
}

export function useConnectPromptShown(): boolean {
  return useSyncExternalStore(
    (f) => {
      subs.add(f);
      return () => void subs.delete(f);
    },
    () => shown > 0,
  );
}
