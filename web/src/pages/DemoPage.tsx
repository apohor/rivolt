import { useState, type MouseEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import ActivityPage from "./ActivityPage";
import MoneyPage from "./MoneyPage";
import NowCard from "../components/NowCard";
import Logo from "../components/Logo";
import { Spinner } from "../components/ui";
import { backend } from "../lib/api";

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

export default function DemoPage() {
  const [tab, setTab] = useState<Tab>("now");
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
  // Detail pages sit outside the demo's read-only allowlist: keep
  // those links inert instead of landing on an error.
  const guardLinks = (e: MouseEvent) => {
    const a = (e.target as HTMLElement).closest("a");
    const href = a?.getAttribute("href") ?? "";
    if (/^\/(drives|charges)\//.test(href)) e.preventDefault();
  };

  return (
    <div className="min-h-full bg-neutral-950 text-neutral-100">
      <div role="note" className="border-b border-sky-800/60 bg-sky-950/40 px-4 py-2 text-sm text-sky-100">
        <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-between gap-2">
          <span>
            <span className="font-medium">Sample truck.</span> Generated data, read-only. Connect your Rivian to see
            yours.
          </span>
          <Link to="/login" className="rounded-md bg-emerald-600 px-3 py-1 text-xs font-semibold text-white hover:bg-emerald-500">
            Connect my Rivian
          </Link>
        </div>
      </div>
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
      <main onClickCapture={guardLinks} className="mx-auto max-w-5xl px-4 pb-16">
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
