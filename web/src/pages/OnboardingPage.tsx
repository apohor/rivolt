import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { backend, type AuthUser, type RivianStatus } from "../lib/api";
import Logo from "../components/Logo";
import { RivianAccountPanel } from "../components/RivianAccountPanel";

type Step = {
  id: string;
  title: string;
  body: React.ReactNode;
  cta: string;
  skippable?: boolean;
  skipLabel?: string;
};

const steps: Step[] = [
  {
    id: "connect-rivian",
    title: "Connect your Rivian",
    body: (
      <div className="space-y-3 text-sm leading-relaxed text-neutral-400">
        <p>
          Your Rivolt login and your Rivian account are{" "}
          <span className="text-neutral-200">separate</span> - creating this
          account didn't connect your truck yet. Sign in with your Rivian
          credentials below and Rivolt starts pulling live telemetry, drive
          sessions, and charge history.
        </p>
        <div className="rounded-md border border-amber-900/60 bg-amber-950/30 px-3 py-2.5 text-amber-300/90 text-xs leading-relaxed">
          <span className="font-semibold">Recommended:</span> in the Rivian
          app, create a second <em>Rivian</em> account and add it as an{" "}
          <a
            href="https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md#recommended-dedicated-authorized-driver-account"
            target="_blank"
            rel="noopener noreferrer"
            className="underline hover:text-amber-200"
          >
            Authorized Driver
          </a>{" "}
          on your vehicle, then connect that account here - your primary
          Rivian login stays untouched. (No need for another Rivolt account -
          this one is yours.) See the linked walkthrough for the step-by-step.
        </div>
        <RivianAccountPanel />
      </div>
    ),
    cta: "Next",
    skippable: true,
    skipLabel: "I'll connect later in Settings",
  },
  {
    id: "import-electrafi",
    title: "Import historical drives",
    body: (
      <div className="space-y-3 text-sm leading-relaxed text-neutral-400">
        <p>
          If you've been using ElectraFi, you can backfill your full drive
          history into Rivolt.
        </p>
        <ol className="list-decimal list-inside space-y-1 text-xs text-neutral-500">
          <li>Export your data from the ElectraFi app.</li>
          <li>
            Go to{" "}
            <span className="text-neutral-300">Settings → Import</span> and
            upload the CSV file.
          </li>
          <li>Rivolt will process and merge it with your live data.</li>
        </ol>
        <p className="text-xs text-neutral-600">
          You can always do this later from the Settings page.
        </p>
      </div>
    ),
    cta: "Got it, next",
    skippable: true,
    skipLabel: "Skip for now",
  },
  {
    id: "done",
    title: "You're all set!",
    body: (
      <p className="text-sm leading-relaxed text-neutral-400">
        Your account is ready. Head to the{" "}
        <span className="text-neutral-200">Overview</span> to see your drive and
        charging timeline, or check the{" "}
        <span className="text-neutral-200">Drives</span> page for an
        AI-powered efficiency breakdown of each session.
      </p>
    ),
    cta: "Get started",
  },
];

function StepDots({ current, total }: { current: number; total: number }) {
  return (
    <div className="flex items-center justify-center gap-2 mb-6">
      {Array.from({ length: total }).map((_, i) => (
        <span
          key={i}
          className={`block rounded-full transition-all ${
            i === current
              ? "h-2 w-6 bg-emerald-500"
              : i < current
                ? "h-2 w-2 bg-emerald-800"
                : "h-2 w-2 bg-neutral-700"
          }`}
        />
      ))}
    </div>
  );
}

export default function OnboardingPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [stepIdx, setStepIdx] = useState(0);
  const [completing, setCompleting] = useState(false);

  const step = steps[stepIdx];
  const isLast = stepIdx === steps.length - 1;

  async function finish() {
    setCompleting(true);
    try {
      await backend.completeOnboarding();
      // Optimistically flip the cached me.onboarding_completed so
      // AppLayout's redirect-to-onboarding effect doesn't fire again
      // when we navigate to /settings. invalidateQueries alone marks
      // the query stale but keeps serving the old value until the
      // refetch lands — that race used to bounce users back through
      // onboarding a second time.
      queryClient.setQueryData<AuthUser | null>(
        ["auth", "me"],
        (prev) => (prev ? { ...prev, onboarding_completed: true } : prev),
      );
      await queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
      // Connected during onboarding → land on the Overview. Skipped
      // the connect step → land on Settings#rivian, since every data
      // page is empty until credentials are wired. The status cache
      // is warm here - the embedded RivianAccountPanel polls it.
      const connected = !!queryClient.getQueryData<RivianStatus>([
        "rivian",
        "status",
      ])?.authenticated;
      const dest = connected ? "/" : "/settings#rivian";
      navigate(dest, { replace: true });
    } catch {
      navigate("/settings#rivian", { replace: true });
    } finally {
      setCompleting(false);
    }
  }

  function advance() {
    if (isLast) {
      finish();
    } else {
      setStepIdx((s) => s + 1);
    }
  }

  return (
    <div className="min-h-full flex items-center justify-center px-4 py-10 app-safe-top">
      <div className="w-full max-w-md rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg">
        {/* Header */}
        <div className="mb-6 flex items-center gap-2 text-neutral-100">
          <Logo size={24} className="text-emerald-400" />
          <span className="text-lg font-semibold tracking-tight">Rivolt</span>
          <span className="ml-auto text-xs text-neutral-600">
            Step {stepIdx + 1} of {steps.length}
          </span>
        </div>

        <StepDots current={stepIdx} total={steps.length} />

        {/* Step content */}
        <div className="min-h-44">
          <h2 className="mb-3 text-base font-semibold text-neutral-100">{step.title}</h2>
          {step.body}
        </div>

        {/* Primary CTA */}
        <button
          type="button"
          onClick={advance}
          disabled={completing}
          className="mt-6 w-full rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 disabled:opacity-50"
        >
          {completing ? "Saving…" : step.cta}
        </button>

        {/* Per-step skip link (only on skippable steps) */}
        {step.skippable && (
          <button
            type="button"
            onClick={advance}
            className="mt-3 w-full text-center text-xs text-neutral-600 hover:text-neutral-400"
          >
            {step.skipLabel ?? "Skip"}
          </button>
        )}
      </div>
    </div>
  );
}
