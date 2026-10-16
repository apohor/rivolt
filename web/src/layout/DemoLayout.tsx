import { Outlet, useLocation } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { useSystemTheme } from "../lib/theme";
import { navPrefetch, pageLoaders } from "../lib/pageLoaders";
import Logo from "../components/Logo";
import { ConnectLink, DemoBanner } from "../components/DemoBanner";
import { Spinner } from "../components/ui";
import { activeTab, buildTabs } from "./tabs";
import { BottomTabBar, SideRail, SubNav } from "./TabShell";

// The public sample-truck demo: the same tab shell as the signed-in app
// (Now · Activity · Money · Plan · Truck), rendering the real pages under
// /demo/* in read-only demo mode. Nothing here needs a session: no
// whoami, no Rivian status, no impersonation.

function prefetchRoute(to: string) {
  const key = navPrefetch[to.replace(/^\/demo/, "") || "/"];
  if (key) void pageLoaders[key]();
}

export default function DemoLayout() {
  useSystemTheme(true);
  const location = useLocation();
  const demo = useQuery({ queryKey: ["demo", "info"], queryFn: () => backend.demoInfo(), retry: 0 });

  if (demo.isLoading) {
    return (
      <div className="p-6">
        <Spinner />
      </div>
    );
  }
  if (!demo.data) {
    return (
      <div className="mx-auto max-w-md p-6 text-center">
        <p className="text-sm text-neutral-300">The demo isn&apos;t available on this server.</p>
        <ConnectLink kind="login" className="mt-3 inline-block text-sm text-emerald-300 hover:underline">
          Sign in
        </ConnectLink>
      </div>
    );
  }

  const tabs = buildTabs({ tripPlannerEnabled: true, isAdmin: false, liveAvailable: false, base: "/demo" });
  const current = activeTab(tabs, location.pathname);
  return (
    <div className="min-h-full flex flex-col">
      <DemoBanner />
      <header className="border-b border-neutral-800 bg-neutral-950/80 backdrop-blur-sm sticky top-0 z-1100 app-safe-top">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3">
          <Link to="/demo" className="flex items-center gap-2 font-semibold tracking-tight text-neutral-100 hover:text-emerald-300 transition-colors shrink-0">
            <Logo size={22} className="text-emerald-400" />
            <span>Rivolt demo</span>
          </Link>
          <h1 className="sr-only">Rivolt demo</h1>
        </div>
      </header>
      <div className="mx-auto flex w-full max-w-6xl flex-1 gap-6 px-4">
        <SideRail tabs={tabs} active={current?.key} onPrefetch={prefetchRoute} />
        <main className="min-w-0 flex-1 py-4 pb-24 md:py-6 md:pb-6">
          <SubNav tab={current} onPrefetch={prefetchRoute} />
          <p className="pb-2 text-[11px] text-neutral-500">Times shown in Pacific time.</p>
          <Outlet />
        </main>
      </div>
      <div className="pb-16 md:pb-0">
        <footer className="border-t border-neutral-900 bg-neutral-950/80 text-xs text-neutral-500">
          <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-center gap-x-3 gap-y-1 px-4 py-3 text-center">
            <a href="https://github.com/apohor/rivolt" target="_blank" rel="noopener noreferrer" className="hover:text-neutral-200">
              GitHub
            </a>
            <span className="text-neutral-700">·</span>
            <span>MIT licensed</span>
          </div>
        </footer>
      </div>
      <BottomTabBar tabs={tabs} active={current?.key} onPrefetch={prefetchRoute} />
    </div>
  );
}
