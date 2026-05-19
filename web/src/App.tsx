import { Route, Routes, useLocation } from "react-router-dom";
import AppLayout from "./layout/AppLayout";
import { ErrorBoundary } from "./components/ui";
import HomePage from "./pages/HomePage";
import DrivesPage from "./pages/DrivesPage";
import DriveDetailPage from "./pages/DriveDetailPage";
import ChargesPage from "./pages/ChargesPage";
import ChargeDetailPage from "./pages/ChargeDetailPage";
import LivePage from "./pages/LivePage";
import TripPlanPage from "./pages/TripPlanPage";
import SettingsPage from "./pages/SettingsPage";
import AdminPage from "./pages/AdminPage";
import LoginPage from "./pages/LoginPage";
import HydraLoginPage from "./pages/HydraLoginPage";
import SignupPage from "./pages/SignupPage";
import OnboardingPage from "./pages/OnboardingPage";
import NotFoundPage from "./pages/NotFoundPage";
import { useTripPlannerEnabled } from "./lib/config";

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
    </ErrorBoundary>
  );
}
