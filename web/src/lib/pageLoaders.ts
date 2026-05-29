// Dynamic-import factories for the route pages, shared between App's
// React.lazy wiring and AppLayout's nav prefetch. Calling a loader
// warms its chunk; the bundler dedupes repeat calls, so triggering on
// hover/focus means the chunk is usually cached by the time the user
// clicks - no Suspense flash on navigation.

export const pageLoaders = {
  home: () => import("../pages/HomePage"),
  drives: () => import("../pages/DrivesPage"),
  driveDetail: () => import("../pages/DriveDetailPage"),
  charges: () => import("../pages/ChargesPage"),
  chargeDetail: () => import("../pages/ChargeDetailPage"),
  live: () => import("../pages/LivePage"),
  tripPlan: () => import("../pages/TripPlanPage"),
  settings: () => import("../pages/SettingsPage"),
  admin: () => import("../pages/AdminPage"),
  login: () => import("../pages/LoginPage"),
  hydraLogin: () => import("../pages/HydraLoginPage"),
  signup: () => import("../pages/SignupPage"),
  onboarding: () => import("../pages/OnboardingPage"),
  notFound: () => import("../pages/NotFoundPage"),
} as const;

export type PageKey = keyof typeof pageLoaders;

// Maps a nav destination path to the chunk it renders, so the header
// can prefetch the right loader when a link is hovered or focused.
export const navPrefetch: Record<string, PageKey> = {
  "/": "home",
  "/live": "live",
  "/drives": "drives",
  "/charges": "charges",
  "/trips/plan": "tripPlan",
  "/settings": "settings",
  "/admin": "admin",
};
