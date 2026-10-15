import { useEffect, useRef, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import ActivityPage from "./ActivityPage";
import MoneyPage from "./MoneyPage";
import NowCard from "../components/NowCard";
import Logo from "../components/Logo";
import { Spinner } from "../components/ui";
import { backend } from "../lib/api";
import { useSignupEnabled } from "../lib/config";
import { track } from "../lib/track";

// Public sample-truck demo (docs/product/sample-truck.md, contract
// demo-contract.md): the real Now, Money and Activity screens against a
// generated truck, read-only, no login. Preview only; /api/demo is 404
// unless the install turned the demo on. Nothing here writes: the
// allowlist is GET-only, and links into detail pages (outside the
// allowlist) are inert.

type Tab = "now" | "money" | "activity";
const TABS: { key: Tab; label: string }[] = [
  { key: "now", label: "Now" },
  { key: "money", label: "Money" },
  { key: "activity", label: "Activity" },
];

export function DemoBanner() {
  // Sign-ups closed: "Connect with a driver account" goes to sign in instead of a
  // signup page that would only say it's closed. null (loading) behaves
  // as open.
  const signupEnabled = useSignupEnabled();
  const to = signupEnabled === false ? "/login" : "/signup";
  return (
      <div role="note" className="border-b border-sky-800/60 bg-sky-950/40 px-4 py-2 text-sm text-sky-100">
        <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-between gap-2">
          <span>
            <span className="font-medium">Sample truck.</span> Generated data, read-only. Connect your Rivian to see
            yours.
          </span>
          <Link to={to} className="rounded-md bg-emerald-600 px-3 py-1 text-xs font-semibold text-white hover:bg-emerald-500">
            Connect with a driver account
          </Link>
        </div>
      </div>
  );
}

// DemoDetail wraps a charge or drive detail page for the signed-out
// demo: the same sample banner and a way back to the demo.
export function DemoDetail({ children }: { children: ReactNode }) {
  return (
    <div className="min-h-full bg-neutral-950 text-neutral-100">
      <DemoBanner />
      <main className="mx-auto max-w-5xl px-4 py-4 pb-16">
        <Link to="/demo" className="mb-3 inline-block text-sm text-neutral-400 hover:text-neutral-100">
          ← Back to the demo
        </Link>
        {children}
      </main>
    </div>
  );
}

export default function DemoPage() {
  // The tab lives in the URL so back and refresh from a detail page
  // land on the same tab.
  const [params, setParams] = useSearchParams();
  const raw = params.get("tab");
  const tab: Tab = raw === "money" || raw === "activity" ? raw : "now";
  const setTab = (t: Tab) => {
    track("demo_tab", { tab: t });
    setParams(t === "now" ? {} : { tab: t }, { replace: true });
  };
  // demo_view: the first screen, once per page load.
  const viewed = useRef(false);
  useEffect(() => {
    if (viewed.current) return;
    viewed.current = true;
    track("demo_view", { tab });
  }, [tab]);
  const demo = useQuery({ queryKey: ["demo", "info"], queryFn: () => backend.demoInfo(), retry: 0 });
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    enabled: !!demo.data,
    retry: 1,
  });

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
        <Link to="/login" className="mt-3 inline-block text-sm text-emerald-300 hover:underline">
          Sign in
        </Link>
      </div>
    );
  }

  const v = vehicles.data?.find((x) => x.id === demo.data!.vehicle_id) ?? vehicles.data?.[0];

  return (
    <div className="min-h-full bg-neutral-950 text-neutral-100">
      <DemoBanner />
      <header className="mx-auto flex max-w-5xl items-center justify-between px-4 py-3">
        <h1 className="flex items-center gap-2 font-semibold tracking-tight">
          <Logo size={22} className="text-emerald-400" />
          Rivolt demo
        </h1>
        <nav aria-label="Demo sections" className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-sm">
          {TABS.map((t) => (
            <button
              key={t.key}
              type="button"
              aria-current={tab === t.key ? "page" : undefined}
              onClick={() => setTab(t.key)}
              className={`rounded-md px-3 py-1.5 ${tab === t.key ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
            >
              {t.label}
            </button>
          ))}
        </nav>
      </header>
      <main className="mx-auto max-w-5xl px-4 pb-16">
        {tab === "now" && (
          <NowCard
            vehicleID={demo.data.vehicle_id}
            vehicleName={v?.name || "Sample truck"}
            packKwh={v?.pack_kwh || null}
            showTelemetryLink={false}
          />
        )}
        {tab === "money" && <MoneyPage />}
        {tab === "activity" && <ActivityPage />}
      </main>
    </div>
  );
}
