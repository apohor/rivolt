import { Route, Routes } from "react-router-dom";
import AppLayout from "./layout/AppLayout";
import HomePage from "./pages/HomePage";
import ProfilesPage from "./pages/ProfilesPage";
import ProfileDetailPage from "./pages/ProfileDetailPage";
import BeansPage from "./pages/BeansPage";
import SettingsPage from "./pages/SettingsPage";
import HistoryPage from "./pages/HistoryPage";
import ShotDetailPage from "./pages/ShotDetailPage";
import LivePage from "./pages/LivePage";
import NotFoundPage from "./pages/NotFoundPage";

export default function App() {
  return (
    <Routes>
      <Route element={<AppLayout />}>
        <Route index element={<HomePage />} />
        <Route path="profiles" element={<ProfilesPage />} />
        <Route path="profiles/:id" element={<ProfileDetailPage />} />
        <Route path="beans" element={<BeansPage />} />
        <Route path="history" element={<HistoryPage />} />
        <Route path="history/:id" element={<ShotDetailPage />} />
        <Route path="live" element={<LivePage />} />
        <Route path="settings" element={<SettingsPage />} />
        {/* Legacy /preheat now lives under Settings. */}
        <Route path="preheat" element={<SettingsPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}
