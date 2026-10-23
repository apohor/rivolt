import { Navigate, Outlet } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Spinner } from "../components/ui";
import Logo from "../components/Logo";
import { SignOutButton } from "../components/SignOutButton";
import { useSystemTheme } from "../lib/theme";

// The shell of the admin site (features.admin_only): a header with the name and sign-out, and the
// page. No user navigation, no status pill, no footer: nothing here fetches user data (the admin
// site answers only /api/admin, /api/auth, /api/health and /api/config).
export function AdminOnlyShell() {
  useSystemTheme(true);
  // A visitor who got through the edge but has no Rivolt session (whoami answers "nobody") is sent
  // to sign in and brought back to /admin, instead of a bare "not authorized". A signed-in
  // non-admin still gets the admin page's own "not authorized".
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  if (me.isLoading) return <div className="p-4"><Spinner /></div>;
  if (me.data === null) return <Navigate to="/login?next=/admin" replace />;
  return (
    <div data-admin-only-shell className="flex min-h-full flex-col">
      <header className="sticky top-0 z-1100 border-b border-neutral-800 bg-neutral-950/80 backdrop-blur-sm app-safe-top">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3">
          <span className="flex items-center gap-2 font-semibold tracking-tight text-neutral-100">
            <Logo size={22} className="text-emerald-400" />
            <span>Rivolt Admin</span>
          </span>
          <SignOutButton />
        </div>
      </header>
      <main data-app-main className="mx-auto w-full max-w-6xl flex-1 px-4 py-4 md:py-6">
        <Outlet />
      </main>
    </div>
  );
}
