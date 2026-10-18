import { Outlet } from "react-router-dom";
import Logo from "../components/Logo";
import { SignOutButton } from "../components/SignOutButton";
import { useSystemTheme } from "../lib/theme";
import { useApplyUiNext } from "../lib/uiNext";

// The shell of the admin site (features.admin_only): a header with the name and sign-out, and the
// page. No user navigation, no status pill, no footer: nothing here fetches user data (the admin
// site answers only /api/admin, /api/auth, /api/health and /api/config).
export function AdminOnlyShell() {
  useSystemTheme(true);
  useApplyUiNext();
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
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-4 md:py-6">
        <Outlet />
      </main>
    </div>
  );
}
