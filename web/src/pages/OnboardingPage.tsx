import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { backend } from "../lib/api";
import Logo from "../components/Logo";

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
          Rivolt connects to Rivian's servers using your account credentials to
          pull live telemetry, drive sessions, and charge history.
        </p>
        <div className="rounded-md border border-amber-900/60 bg-amber-950/30 px-3 py-2.5 text-amber-300/90 text-xs leading-relaxed">
          <span className="font-semibold">Recommended:</span> create a dedicated
          Rivian account and add it as an{" "}
          <a
            href="https://rivian.com/support/article/can-i-grant-others-access-to-my-app"
            target="_blank"
            rel="noopener noreferrer"
            className="underline hover:text-amber-200"
          >
            Authorized Driver
          </a>{" "}
          on your vehicle. That way Rivolt uses its own credentials and your
          primary account stays separate.
        </div>
        <p>
          Once the account is set up, go to{" "}
          <span className="text-neutral-200">Settings → Rivian</span> and sign
          in with those credentials.
        </p>
      </div>
    ),
    cta: "Got it, next",
    skippable: true,
    skipLabel: "I'll set this up later",
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
      await queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
      navigate("/", { replace: true });
    } catch {
      navigate("/", { replace: true });
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
      <div className="w-full max-w-sm rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg">
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
        <div className="min-h-[11rem]">
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
