import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AuthUser } from "../lib/api";
import Logo from "../components/Logo";
import {
  AuthorizedDriverNote,
  RivianAccountPanel,
} from "../components/RivianAccountPanel";

// OnboardingPage is the single first-run step: connect a Rivian
// account. It used to be a three-step wizard (connect, ElectraFi
// import, "you're all set"), but nearly every signup that never added a
// vehicle stopped on the connect step, and the wizard's "Next" button
// silently skipped it. Now the only way forward is the sign-in form
// itself; "I'll connect later" is the explicit skip. The ElectraFi
// import is offered on the Overview once there's a vehicle to import
// into.
export default function OnboardingPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [completing, setCompleting] = useState(false);
  // Shares its cache with RivianAccountPanel's own status query.
  const status = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    staleTime: 30_000,
  });
  const connected = !!status.data?.authenticated;

  async function finish() {
    setCompleting(true);
    try {
      await backend.completeOnboarding();
      // Optimistically flip the cached me.onboarding_completed so
      // AppLayout's redirect-to-onboarding effect doesn't fire again
      // when we navigate away. invalidateQueries alone marks the query
      // stale but keeps serving the old value until the refetch lands
      // — that race used to bounce users back through onboarding a
      // second time.
      queryClient.setQueryData<AuthUser | null>(
        ["auth", "me"],
        (prev) => (prev ? { ...prev, onboarding_completed: true } : prev),
      );
      await queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
    } finally {
      setCompleting(false);
      // The Overview carries the connect form until a Rivian account
      // is linked, so it's the right landing page either way.
      navigate("/", { replace: true });
    }
  }

  return (
    <div className="min-h-full flex items-center justify-center px-4 py-10 app-safe-top">
      <div className="w-full max-w-md rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg">
        <div className="mb-6 flex items-center gap-2 text-neutral-100">
          <Logo size={24} className="text-emerald-400" />
          <span className="text-lg font-semibold tracking-tight">Rivolt</span>
        </div>

        <h2 className="mb-3 text-base font-semibold text-neutral-100">
          Connect your Rivian
        </h2>
        <div className="space-y-3 text-sm leading-relaxed text-neutral-400">
          <p>
            Sign in with the same email and password you use in the Rivian
            app and Rivolt starts pulling live telemetry, drive sessions, and
            charge history.
          </p>
          <RivianAccountPanel />
          {!connected && <AuthorizedDriverNote />}
        </div>

        {connected ? (
          <button
            type="button"
            onClick={finish}
            disabled={completing}
            className="mt-6 w-full rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 disabled:opacity-50"
          >
            {completing ? "Saving…" : "Go to Overview"}
          </button>
        ) : (
          <button
            type="button"
            onClick={finish}
            disabled={completing}
            className="mt-6 w-full text-center text-xs text-neutral-600 hover:text-neutral-400 disabled:opacity-50"
          >
            I'll connect later
          </button>
        )}
      </div>
    </div>
  );
}
