// The browser's "install this app" prompt, kept for later. Chromium fires beforeinstallprompt once, early,
// and only a tap can show it, so it is caught here at load and handed to whoever asks.
type InstallEvent = Event & { prompt: () => Promise<void>; userChoice: Promise<{ outcome: "accepted" | "dismissed" }> };

let held: InstallEvent | null = null;
const listeners = new Set<() => void>();

if (typeof window !== "undefined") {
  window.addEventListener("beforeinstallprompt", (e) => {
    e.preventDefault();
    held = e as InstallEvent;
    listeners.forEach((l) => l());
  });
  window.addEventListener("appinstalled", () => {
    held = null;
    listeners.forEach((l) => l());
  });
}

export const canInstall = (): boolean => held !== null;

export function onInstallChange(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

// Shows the prompt (from a tap). Resolves whether the user accepted.
export async function promptInstall(): Promise<boolean> {
  const e = held;
  if (!e) return false;
  await e.prompt();
  const { outcome } = await e.userChoice;
  held = null;
  listeners.forEach((l) => l());
  return outcome === "accepted";
}

// iPhone and iPad only offer push to an app that has been added to the home screen.
export const isIOSNotInstalled = (): boolean =>
  typeof navigator !== "undefined" &&
  /iPad|iPhone|iPod/.test(navigator.userAgent) &&
  !(navigator as Navigator & { standalone?: boolean }).standalone;
