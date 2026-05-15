import { useEffect } from "react";
import { NavLink, Outlet, useNavigate, useLocation } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { useTripPlannerEnabled } from "../lib/config";
import Logo from "../components/Logo";
import IOSInstallBanner from "../components/IOSInstallBanner";

const nav: { to: string; label: string; end?: boolean }[] = [
  { to: "/", label: "Overview", end: true },
  { to: "/live", label: "Live" },
  { to: "/drives", label: "Drives" },
  { to: "/charges", label: "Charges" },
  { to: "/settings", label: "Settings" },
];

// Plan is gated on the trip-planner feature flag; spliced in
// before Settings when enabled so the nav order reads
// Overview → Live → Drives → Charges → Plan → Settings.
const planNavItem: { to: string; label: string; end?: boolean } = {
  to: "/trips/plan",
  label: "Plan",
};

// adminNav is appended to nav at render-time when whoami() reports
// role === "admin". Kept as a separate const so the role check
// lives in exactly one place.
const adminNav: { to: string; label: string; end?: boolean }[] = [
  { to: "/admin", label: "Admin" },
];

// StatusPill reflects the Rivian connection state, since that's what
// the user actually cares about from the header. Five states:
//   - backend unreachable   → red "offline"
//   - backend ok, no live   → neutral "no rivian" (stub build)
//   - backend ok, signed-out → neutral "not connected"
//   - signed in, MFA pending → amber "mfa pending"
//   - fully authenticated    → green "connected"
// The backend's own health is implicit: if we can't even reach it we
// go red; in every other case the backend is fine so we don't clutter
// the header saying so.
function StatusPill() {
  const health = useQuery({
    queryKey: ["health"],
    queryFn: () => backend.health(),
    refetchInterval: 15_000,
  });
  const rivian = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    refetchInterval: 15_000,
    enabled: !!health.data?.ok,
  });

  let label = "checking…";
  let tone = "bg-neutral-800 text-neutral-400 border-neutral-700";
  let title: string | undefined;

  if (health.isError) {
    label = "offline";
    tone = "bg-rose-900/40 text-rose-300 border-rose-800";
  } else if (health.data?.ok) {
    title = `Rivolt ${health.data.version}`;
    if (!rivian.data) {
      // Backend is up; rivian status still in flight — keep neutral.
    } else if (!rivian.data.enabled) {
      label = "no rivian";
    } else if (rivian.data.mfa_pending) {
      label = "mfa pending";
      tone = "bg-amber-900/40 text-amber-300 border-amber-800";
    } else if (rivian.data.authenticated) {
      label = "connected";
      tone = "bg-emerald-900/40 text-emerald-300 border-emerald-800";
      if (rivian.data.email) title = `${rivian.data.email} · ${title}`;
    } else {
      label = "not connected";
    }
  }
  return (
    <span className={`rounded-full border px-3 py-1 text-xs ${tone}`} title={title}>
      {label}
    </span>
  );
}

// SignOutButton hides itself when auth is disabled on the server
// (whoami returns null). When present, clicking it clears the
// session cookie server-side and sends the browser to /login.
// Using window.location instead of react-router so the full SPA
// state is dropped — we don't want cached queries leaking between
// users on a shared-browser install.
function SignOutButton() {
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => backend.whoami(),
    staleTime: 5 * 60_000,
  });
  if (!me.data) return null;
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await backend.logout();
        } finally {
          window.location.assign("/login");
        }
      }}
      title={`Signed in as ${me.data.username}`}
      className="rounded-full border border-neutral-800 bg-neutral-900 px-3 py-1 text-xs text-neutral-300 hover:border-neutral-700 hover:text-neutral-100"
    >
      Sign out
    </button>
  );
}

export default function AppLayout() {
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
  useEffect(() => {
    if (me.data && me.data.onboarding_completed === false) {
      navigate("/onboarding", { replace: true });
    }
  }, [me.data, navigate, location.pathname]);

  const tripPlannerEnabled = useTripPlannerEnabled();
  const baseNav = tripPlannerEnabled
    ? [...nav.slice(0, -1), planNavItem, nav[nav.length - 1]]
    : nav;
  const navItems = me.data?.role === "admin" ? [...baseNav, ...adminNav] : baseNav;
  return (
    <div className="min-h-full flex flex-col">
      <IOSInstallBanner />
      <header className="border-b border-neutral-800 bg-neutral-950/80 backdrop-blur sticky top-0 z-[1100] app-safe-top">
        <div className="mx-auto max-w-5xl px-4 py-3 flex flex-wrap items-center gap-x-4 gap-y-2 sm:flex-nowrap sm:justify-between">
          <NavLink
            to="/"
            className="flex items-center gap-2 font-semibold tracking-tight text-neutral-100 hover:text-emerald-300 transition-colors shrink-0"
          >
            <Logo size={22} className="text-emerald-400" />
            <span>Rivolt</span>
          </NavLink>
          <div className="ml-auto sm:order-last sm:ml-0 flex items-center gap-2">
            <StatusPill />
            <SignOutButton />
          </div>
          <nav className="order-last w-full sm:order-none sm:w-auto" aria-label="Primary">
            <ul className="flex items-center justify-between gap-0.5 sm:justify-start sm:gap-1">
              {navItems.map((n) => (
                <li key={n.to}>
                  <NavLink
                    to={n.to}
                    end={n.end}
                    className={({ isActive }) =>
                      `block rounded-md px-2 py-1.5 text-sm transition-colors sm:px-3 ${
                        isActive
                          ? "bg-neutral-800 text-neutral-100"
                          : "text-neutral-400 hover:text-neutral-100 hover:bg-neutral-900"
                      }`
                    }
                  >
                    {n.label}
                  </NavLink>
                </li>
              ))}
            </ul>
          </nav>
        </div>
      </header>
      <main className="flex-1 mx-auto w-full max-w-5xl px-4 py-4 md:py-5 lg:py-6">
        <Outlet />
      </main>
      <Footer />
    </div>
  );
}

function Footer() {
  const health = useQuery({
    queryKey: ["health"],
    queryFn: () => backend.health(),
    staleTime: 60_000,
  });
  return (
    <footer className="border-t border-neutral-900 bg-neutral-950/80 text-xs text-neutral-500">
      <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-center gap-x-3 gap-y-1 px-4 py-3 text-center">
        <a
          href="https://github.com/apohor/rivolt"
          target="_blank"
          rel="noopener noreferrer"
          className="hover:text-neutral-200"
        >
          GitHub
        </a>
        <span className="text-neutral-700">·</span>
        <span>MIT licensed</span>
        <span className="text-neutral-700">·</span>
        <a
          href="https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md"
          target="_blank"
          rel="noopener noreferrer"
          className="hover:text-neutral-200"
        >
          Docs
        </a>
        <span className="text-neutral-700">·</span>
        <a
          href="https://discord.gg/kdKqbK3pz"
          target="_blank"
          rel="noopener noreferrer"
          className="hover:text-neutral-200"
        >
          Discord
        </a>
        {health.data?.version && (
          <>
            <span className="text-neutral-700">·</span>
            {/* Link the running version to its GitHub release page
                so the user can read the changelog for what's
                currently deployed. Tag format mirrors what CI
                pushes: vX.Y.Z (semver). */}
            <a
              href={`https://github.com/apohor/rivolt/releases/tag/v${health.data.version}`}
              target="_blank"
              rel="noopener noreferrer"
              className="font-mono text-neutral-600 hover:text-neutral-300"
              title="What changed in this release"
            >
              v{health.data.version}
            </a>
          </>
        )}
      </div>
    </footer>
  );
}
