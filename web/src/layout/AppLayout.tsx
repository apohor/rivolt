import { NavLink, Outlet } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import Logo from "../components/Logo";

const nav = [
  { to: "/", label: "Home", end: true },
  { to: "/live", label: "Live" },
  { to: "/profiles", label: "Profiles" },
  { to: "/history", label: "Shots" },
  { to: "/beans", label: "Beans" },
  { to: "/settings", label: "Settings" },
];

function StatusPill() {
  const { data, isLoading } = useQuery({
    queryKey: ["machine-status"],
    queryFn: () => backend.machineStatus(),
    refetchInterval: 10_000,
  });

  let label = "checking…";
  let tone = "bg-neutral-800 text-neutral-400";
  let title = data?.machine_url;
  if (!isLoading && data) {
    if (data.reachable && !data.degraded) {
      label = "machine online";
      tone = "bg-emerald-900/40 text-emerald-300 border-emerald-800";
    } else if (data.reachable && data.degraded) {
      // Probe just failed but the machine answered recently — typically a
      // burst of wifi TX retries on the machine side. Don't alarm the user.
      label = "machine spotty";
      tone = "bg-amber-900/40 text-amber-300 border-amber-800";
      title = `${data.machine_url} — probe retrying (${data.attempts ?? 0} attempts)`;
    } else {
      label = "machine unreachable";
      tone = "bg-rose-900/40 text-rose-300 border-rose-800";
    }
  }
  return (
    <span
      className={`rounded-full border border-neutral-800 px-3 py-1 text-xs ${tone}`}
      title={title}
    >
      {label}
    </span>
  );
}

export default function AppLayout() {
  return (
    <div className="min-h-full flex flex-col">
      <header className="border-b border-neutral-800 bg-neutral-950/80 backdrop-blur sticky top-0 z-10 app-safe-top">
        <div className="mx-auto max-w-5xl px-4 py-3 flex flex-wrap items-center gap-x-4 gap-y-2 sm:flex-nowrap sm:justify-between">
          <NavLink
            to="/"
            className="flex items-center gap-2 font-semibold tracking-tight text-neutral-100 hover:text-emerald-300 transition-colors shrink-0"
          >
            <Logo size={22} className="text-emerald-400" />
            <span>Caffeine</span>
          </NavLink>
          {/* Status pill sits next to the logo on mobile (second slot on
              the top row) so the scrollable nav can claim the whole
              second row. On >=sm it collapses into the single-row
              layout on the right. */}
          <div className="ml-auto sm:order-last sm:ml-0">
            <StatusPill />
          </div>
          {/* Nav fills the second row on mobile (spread evenly so every
              page is reachable without a horizontal scroll) and collapses
              to the normal single-row layout from sm up. */}
          <nav
            className="order-last w-full sm:order-none sm:w-auto"
            aria-label="Primary"
          >
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
