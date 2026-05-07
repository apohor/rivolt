import { Route, Routes } from "react-router-dom";
import AppLayout from "./layout/AppLayout";
import HomePage from "./pages/HomePage";
import DrivesPage from "./pages/DrivesPage";
import DriveDetailPage from "./pages/DriveDetailPage";
import ChargesPage from "./pages/ChargesPage";
import ChargeDetailPage from "./pages/ChargeDetailPage";
import LivePage from "./pages/LivePage";
import SettingsPage from "./pages/SettingsPage";
import AdminPage from "./pages/AdminPage";
import LoginPage from "./pages/LoginPage";
import HydraLoginPage from "./pages/HydraLoginPage";
import SignupPage from "./pages/SignupPage";
import OnboardingPage from "./pages/OnboardingPage";
import NotFoundPage from "./pages/NotFoundPage";

export default function App() {
  return (
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
      <Route path="onboarding" element={<OnboardingPage />} />
      <Route element={<AppLayout />}>
        <Route index element={<HomePage />} />
        <Route path="drives" element={<DrivesPage />} />
        <Route path="drives/:id" element={<DriveDetailPage />} />
        <Route path="charges" element={<ChargesPage />} />
        <Route path="charges/:id" element={<ChargeDetailPage />} />
        <Route path="live" element={<LivePage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="admin" element={<AdminPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}
