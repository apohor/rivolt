import { lazy, Suspense } from "react";
import { Route, Routes, useLocation } from "react-router-dom";
import AppLayout from "./layout/AppLayout";
import { ErrorBoundary, Spinner } from "./components/ui";
import { useTripPlannerEnabled } from "./lib/config";
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
const SettingsPage = lazy(pageLoaders.settings);
const AdminPage = lazy(pageLoaders.admin);
const LoginPage = lazy(pageLoaders.login);
const HydraLoginPage = lazy(pageLoaders.hydraLogin);
const SignupPage = lazy(pageLoaders.signup);
const OnboardingPage = lazy(pageLoaders.onboarding);
const NotFoundPage = lazy(pageLoaders.notFound);

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
      <Route element={<AppLayout />}>
        <Route index element={<HomePage />} />
        <Route path="drives" element={<DrivesPage />} />
        <Route path="drives/:id" element={<DriveDetailPage />} />
        <Route path="charges" element={<ChargesPage />} />
        <Route path="charges/:id" element={<ChargeDetailPage />} />
        <Route path="live" element={<LivePage />} />
        <Route path="trips/plan" element={<TripPlanGuard />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="admin" element={<AdminPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
    </Suspense>
    </ErrorBoundary>
  );
}
