// The iPhone app's link, as the server reports it (features.ios_app_url, empty until the app is listed). It is always a direct URL
// to Apple: no redirect, no campaign tokens. An App Store link and a TestFlight link are told apart because only the App Store one
// may appear on a share card (never TestFlight), and the Settings button says which it is.

export type AppLinkKind = "store" | "testflight";

export function appLinkKind(url: string | undefined | null): AppLinkKind | null {
  if (!url) return null;
  try {
    const u = new URL(url);
    if (u.protocol !== "https:" || u.username || u.password) return null;
    if (u.hostname === "apps.apple.com") return "store";
    if (u.hostname === "testflight.apple.com") return "testflight";
  } catch {
    /* not a URL */
  }
  return null;
}

// Growth's wording for the Settings row.
export const APP_ROW_TITLE = "Get the iPhone app";
export const APP_ROW_HELP =
  "Now at a glance, Lock Screen widgets, a Live Activity while charging, and notifications. Signs in with this account; it never sees your Rivian password.";
export const appButtonLabel = (kind: AppLinkKind): string => (kind === "store" ? "Open in the App Store" : "Join the TestFlight beta");

// The share card's first footer line: the app clause only for an App Store link (never TestFlight), else the plain line.
export function shareFooterLine(url: string | undefined | null): string {
  return appLinkKind(url) === "store" ? "Made with Rivolt · rivolt.dev · iPhone app on the App Store" : "Made with Rivolt · rivolt.dev";
}
