import { NavLink, Outlet } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import Logo from "../components/Logo";

const nav = [
  { to: "/", label: "Overview", end: true },
  { to: "/live", label: "Live" },
  { to: "/drives", label: "Drives" },
  { to: "/charges", label: "Charges" },
  { to: "/settings", label: "Settings" },
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

export default function AppLayout() {
  return (
    <div className="min-h-full flex flex-col">
      <header className="border-b border-neutral-800 bg-neutral-950/80 backdrop-blur sticky top-0 z-[1100] app-safe-top">
        <div className="mx-auto max-w-5xl px-4 py-3 flex flex-wrap items-center gap-x-4 gap-y-2 sm:flex-nowrap sm:justify-between">
          <NavLink
            to="/"
            className="flex items-center gap-2 font-semibold tracking-tight text-neutral-100 hover:text-emerald-300 transition-colors shrink-0"
          >
            <Logo size={22} className="text-emerald-400" />
            <span>Rivolt</span>
          </NavLink>
          <div className="ml-auto sm:order-last sm:ml-0">
            <StatusPill />
          </div>
          <nav className="order-last w-full sm:order-none sm:w-auto" aria-label="Primary">
            <ul className="flex items-center justify-between gap-0.5 sm:justify-start sm:gap-1">
              {nav.map((n) => (
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
    </div>
  );
}
