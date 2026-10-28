import { useLandingGate } from "./lib/landingGate";
import { lazy, Suspense } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "./lib/api";
import { inDemo } from "./lib/demo";
import { termsGateActive } from "./lib/terms";
import { useImpersonationTarget } from "./lib/impersonation";
import { TermsGate } from "./components/TermsGate";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import AppLayout from "./layout/AppLayout";
import { ErrorBoundary, Spinner } from "./components/ui";
import { useAdminOnly, useDemoOnly, useTripPlannerEnabled } from "./lib/config";
import { pageLoaders } from "./lib/pageLoaders";

// Route pages are split into their own chunks so the initial bundle
// doesn't carry every page's deps (leaflet, uPlot, markdown) up front;
// each loads on first navigation behind the Suspense fallback below.
// The loaders are shared with AppLayout so the nav can prefetch them.
const HomePage = lazy(pageLoaders.home);
const DrivesPage = lazy(pageLoaders.drives);
const DriveDetailPage = lazy(pageLoaders.driveDetail);
const ChargesPage = lazy(pageLoaders.charges);
const ChargeDetailPage = lazy(pageLoaders.chargeDetail);
const LivePage = lazy(pageLoaders.live);
const TripPlanPage = lazy(pageLoaders.tripPlan);
const MoneyPage = lazy(pageLoaders.money);
const TruckPage = lazy(pageLoaders.truck);
const ActivityPage = lazy(pageLoaders.activity);
const SettingsPage = lazy(pageLoaders.settings);
const AdminPage = lazy(pageLoaders.admin);
const LoginPage = lazy(pageLoaders.login);
const PrivacyPage = lazy(() => import("./pages/PrivacyPage"));
const ChangelogPage = lazy(() => import("./pages/ChangelogPage"));
const ChargingNetworksPage = lazy(() => import("./pages/ChargingNetworksPage"));
const R2GuidePage = lazy(() => import("./pages/R2GuidePage"));
const SampleTripPage = lazy(() => import("./pages/SampleTripPage"));
const SampleTripsHubPage = lazy(() => import("./pages/SampleTripPage").then((m) => ({ default: m.SampleTripsHubPage })));
const TermsPage = lazy(() => import("./pages/TermsPage"));
const MovingFromElectraFiPage = lazy(() => import("./pages/MovingFromElectraFiPage"));
const SafeToConnectPage = lazy(() => import("./pages/SafeToConnectPage"));
const SupportPage = lazy(() => import("./pages/SupportPage"));
const LeaseCalculatorPage = lazy(() => import("./pages/LeaseCalculatorPage"));
const ChargingCostPage = lazy(() => import("./pages/ChargingCostPage"));
const HydraLoginPage = lazy(pageLoaders.hydraLogin);
const SignupPage = lazy(pageLoaders.signup);
const OnboardingPage = lazy(pageLoaders.onboarding);
const NotFoundPage = lazy(pageLoaders.notFound);
const LandingPage = lazy(pageLoaders.landing);
const DemoIndex = lazy(pageLoaders.demo);
const DemoLayout = lazy(() => import("./layout/DemoLayout"));

// Paths the admin site serves: the admin page, sign-in and the OIDC hand-off.
export function adminSitePath(p: string): boolean {
  return p === "/admin" || p.startsWith("/admin/") || p === "/login" || p.startsWith("/auth/");
}

// TripPlanGuard renders the planner page only when the feature
// flag is on. When off it falls through to the 404 — same surface
// the server presents on the API side, so a flipped-off planner
// is indistinguishable from a deploy that never had the route.
function TripPlanGuard() {
  const enabled = useTripPlannerEnabled();
  return enabled ? <TripPlanPage /> : <NotFoundPage />;
}

export default function App() {
  const location = useLocation();
  // The standalone demo deployment (features.demo_only): everything outside
  // /demo (landing, login, signup, onboarding, the app routes) goes to /demo.
  const demoOnly = useDemoOnly();
  const adminOnly = useAdminOnly();
  // Signed-out visitors at "/" see the landing page when the install
  // enables it (RIVOLT_LANDING); otherwise this is a pass-through.
  const landing = useLandingGate(demoOnly.only ? "/demo" : location.pathname);
  // Signed-in users who have not accepted the current Terms see the "I agree" step first.
  // The demo-only deployment never calls /api/auth/me, so ask only once we know it is not that host.
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => backend.whoami(),
    staleTime: 5 * 60_000,
    enabled: !demoOnly.loading && !demoOnly.only && !inDemo(),
  });
  // An admin viewing as a user has not accepted anything for them and cannot (read-only).
  const impersonating = useImpersonationTarget();
  const gate = !impersonating && termsGateActive(me.data, location.pathname);
  // The Rivian connect step must not show before the gate has had its say.
  const meLoading = location.pathname.startsWith("/onboarding") && me.isPending && me.fetchStatus !== "idle";
  if (demoOnly.loading || adminOnly.loading || landing === "loading" || meLoading) return <div className="p-4"><Spinner /></div>;
  // The admin site serves the admin page and sign-in only; everything else goes to /admin. While an
  // admin is viewing as a user the user pages render as on rivolt.dev.
  if (adminOnly.only && !impersonating && !adminSitePath(location.pathname)) return <Navigate to="/admin" replace />;
  if (demoOnly.only && !location.pathname.startsWith("/demo") && location.pathname !== "/privacy" && location.pathname !== "/terms") return <Navigate to="/demo" replace />;
  if (gate && me.data?.terms) return <TermsGate terms={me.data.terms} />;
  if (landing === "landing") {
    return (
      <Suspense fallback={<div className="p-4"><Spinner /></div>}>
        <LandingPage />
      </Suspense>
    );
  }
  return (
    <ErrorBoundary
      // Reset on navigation — a crash on one route shouldn't lock
      // the user out of every other route. Without this, the
      // fallback persists across links because nothing tells the
      // boundary that the user moved on.
      resetKey={location.pathname}
    >
    <Suspense fallback={<div className="p-4"><Spinner /></div>}>
    <Routes>
      {/*
        /login, /signup and /onboarding sit outside AppLayout so the
        layout's status pill and nav don't fire /api/* calls before the
        user has a session. Once login succeeds the API client's normal
        401 handler takes over inside the AppLayout tree.
      */}
      <Route path="login" element={<LoginPage />} />
      <Route path="privacy" element={<PrivacyPage />} />
      <Route path="changelog" element={<ChangelogPage />} />
      <Route path="sample-trips" element={<SampleTripsHubPage />} />
      <Route path="sample-trips/:slug" element={<SampleTripPage />} />
      <Route path="terms" element={<TermsPage />} />
      <Route path="moving-from-electrafi" element={<MovingFromElectraFiPage />} />
      <Route path="safe-to-connect" element={<SafeToConnectPage />} />
      <Route path="support" element={<SupportPage />} />
      <Route path="r2" element={<R2GuidePage />} />
      <Route path="charging-networks" element={<ChargingNetworksPage />} />
      <Route path="lease-calculator" element={<LeaseCalculatorPage />} />
      <Route path="charging-cost" element={<ChargingCostPage />} />
      {/*
        /auth/hydra/login is the URL Hydra redirects the browser to
        after a third-party app initiates an OIDC code flow. It sits
        outside AppLayout for the same reasons /login does: no nav,
        no API calls before the user is identified.
      */}
      <Route path="auth/hydra/login" element={<HydraLoginPage />} />
      <Route path="signup" element={<SignupPage />} />
      <Route path="signup/full" element={<SignupPage />} />
      <Route path="onboarding" element={<OnboardingPage />} />
      <Route path="demo" element={<DemoLayout />}>
        <Route index element={<DemoIndex home={<HomePage />} />} />
        <Route path="activity" element={<ActivityPage />} />
        <Route path="money" element={<MoneyPage />} />
        <Route path="plan" element={<TripPlanPage />} />
        <Route path="trips/plan" element={<TripPlanPage />} />
        <Route path="truck" element={<TruckPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="drives" element={<DrivesPage />} />
        <Route path="drives/:id" element={<DriveDetailPage />} />
        <Route path="charges" element={<ChargesPage />} />
        <Route path="charges/:id" element={<ChargeDetailPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
      <Route element={<AppLayout />}>
        <Route index element={<HomePage />} />
        <Route path="drives" element={<DrivesPage />} />
        <Route path="drives/:id" element={<DriveDetailPage />} />
        <Route path="charges" element={<ChargesPage />} />
        <Route path="charges/:id" element={<ChargeDetailPage />} />
        <Route path="live" element={<LivePage />} />
        <Route path="trips/plan" element={<TripPlanGuard />} />
        <Route path="money" element={<MoneyPage />} />
        <Route path="truck" element={<TruckPage />} />
        <Route path="activity" element={<ActivityPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="admin" element={<AdminPage />} />
        <Route path="admin/users" element={<AdminPage />} />
        <Route path="admin/users/:id" element={<AdminPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
    </Suspense>
    </ErrorBoundary>
  );
}
