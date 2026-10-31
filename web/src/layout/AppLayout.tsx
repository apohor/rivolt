import { useRefreshListsWhenSessionCloses } from "../lib/activityLists";
import { useAccountUnits } from "../lib/accountUnits";
import { rememberAppSeen, useSystemTheme } from "../lib/theme";
import { Footer } from "../components/Footer";
import { useEffect } from "react";
import { NavLink, Outlet, useNavigate, useLocation } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { useAdminOnly, useTripPlannerEnabled } from "../lib/config";
import { navPrefetch, pageLoaders } from "../lib/pageLoaders";
import { useImpersonationTarget } from "../lib/impersonation";
import { ImpersonationBanner } from "../components/ImpersonationBanner";
import { AdminOnlyShell } from "./AdminOnlyShell";
import { Spinner } from "../components/ui";
import Logo from "../components/Logo";
import IOSInstallBanner from "../components/IOSInstallBanner";
import { activeTab, buildTabs } from "./tabs";
import { BottomTabBar, SideRail, SubNav } from "./TabShell";
import DegradedBanner from "./DegradedBanner";
import { PlanNotice } from "../components/PlanNotice";

// Warm a route's lazy chunk before the user clicks. The dynamic
// import is module-cached, so repeat calls are cheap no-ops; firing on
// hover/focus means the chunk is usually ready by click time, removing
// the Suspense flash on first navigation to each page.
function prefetchRoute(to: string) {
  const key = navPrefetch[to];
  if (key) void pageLoaders[key]();
}

function UserLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => backend.whoami(),
    staleTime: 5 * 60_000,
  });

  // Redirect to onboarding stepper when a freshly created account
  // logs in for the first time. The backend back-fills existing users
  // as completed=true in migration 0026, so only brand-new signups
  // will see this. We guard on me.data being loaded (not null) so the
  // redirect doesn't fire in no-auth / dev mode where whoami returns null.
  // Never while an admin is viewing as someone: the target's onboarding can't be completed from a
  // read-only view (POST /api/onboarding/complete is refused), so the redirect would trap the admin.
  const impersonating = useImpersonationTarget();
  // The units follow the account (not while an admin views as someone: that must not change their units).
  useAccountUnits(impersonating ? null : me.data);
  useEffect(() => {
    if (impersonating) return;
    if (me.data && me.data.onboarding_completed === false) {
      navigate("/onboarding", { replace: true });
    }
  }, [me.data, navigate, location.pathname, impersonating]);

  const tripPlannerEnabled = useTripPlannerEnabled();
  useSystemTheme(true);
  useEffect(rememberAppSeen, []);
  const tabs = buildTabs({
    tripPlannerEnabled,
    isAdmin: me.data?.role === "admin",
    liveAvailable: !impersonating,
  });
  const current = activeTab(tabs, location.pathname);
  return (
    <div className="min-h-full flex flex-col">
      <ImpersonationBanner />
      <IOSInstallBanner />
      <header className="border-b border-neutral-800 bg-neutral-950/80 backdrop-blur-sm sticky top-0 z-1100 app-safe-top">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3">
          <NavLink
            to="/"
            className="flex items-center gap-2 font-semibold tracking-tight text-neutral-100 hover:text-emerald-300 transition-colors shrink-0"
          >
            <Logo size={22} className="text-emerald-400" />
            <span>Rivolt</span>
          </NavLink>
        </div>
      </header>
      <div className="mx-auto flex w-full max-w-6xl flex-1 gap-6 px-4">
        <SideRail tabs={tabs} active={current?.key} onPrefetch={prefetchRoute} />
        {/* pb-24 on mobile keeps the last row clear of the fixed tab bar. */}
        <main data-app-main className="min-w-0 flex-1 py-4 pb-[calc(5.5rem+env(safe-area-inset-bottom))] md:py-6 md:pb-6">
          <DegradedBanner />
          <PlanNotice />
          <SubNav tab={current} onPrefetch={prefetchRoute} />
          <Outlet />
        </main>
      </div>
      <div className="pb-16 md:pb-0">
        <Footer />
      </div>
      <BottomTabBar tabs={tabs} active={current?.key} onPrefetch={prefetchRoute} />
    </div>
  );
}


// The layout of the routes: on the admin site (features.admin_only) the admin-only shell, which
// never mounts anything that fetches user data; everywhere else the usual layout. While an admin
// is viewing as a user the user pages render as they do on rivolt.dev.
export default function AppLayout() {
  // The drive and charge lists refresh when a drive or charge ends, wherever the user is.
  useRefreshListsWhenSessionCloses();
  const adminOnly = useAdminOnly();
  const impersonating = useImpersonationTarget();
  if (adminOnly.loading) return <div className="p-4"><Spinner /></div>;
  if (adminOnly.only && !impersonating) return <AdminOnlyShell />;
  return <UserLayout />;
}
