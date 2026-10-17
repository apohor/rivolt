import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError, backend, type TermsState } from "../lib/api";
import Logo from "./Logo";
import { SignOutButton } from "./SignOutButton";
import { ErrorBox } from "./ui";
import { useSystemTheme } from "../lib/theme";

// Full-screen step before the app until the current Terms are accepted. The checkbox starts
// unticked and Continue stays disabled until it is ticked. The Terms and the Privacy Policy open in
// a new tab so nothing here is lost; signing out is always available.
export function TermsGate({ terms }: { terms: TermsState }) {
  useSystemTheme(true);
  const qc = useQueryClient();
  const [agreed, setAgreed] = useState(false);
  const accept = useMutation({
    mutationFn: () => backend.acceptTerms(terms.current),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["auth", "me"] }),
    onError: (e) => {
      // The version moved on while this was open: refetch and show the new one, unticked.
      if (e instanceof ApiError && e.status === 409) {
        setAgreed(false);
        void qc.invalidateQueries({ queryKey: ["auth", "me"] });
      }
    },
  });
  const changed = terms.accepted != null;
  return (
    <div data-terms-gate className="flex min-h-full items-center justify-center bg-neutral-950 px-4 py-10 text-neutral-200">
      <main className="w-full max-w-md rounded-xl border border-neutral-800 bg-neutral-900/60 p-6 shadow-lg">
        <div className="mb-4 flex items-center justify-between gap-3">
          <Logo size={24} className="text-emerald-400" />
          <SignOutButton />
        </div>
        <h1 className="text-xl font-semibold text-neutral-50">{changed ? "We updated our terms" : "One more step"}</h1>
        <p className="mt-2 text-sm leading-relaxed text-neutral-300">
          {changed
            ? "Please review the updated Terms of Service and Privacy Policy before you continue."
            : "Before you use Rivolt, please read how it works and what it records, in the Terms of Service and the Privacy Policy."}
        </p>
        <p className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-sm">
          <a href="/terms" target="_blank" rel="noopener noreferrer" className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200">
            Terms of Service
          </a>
          <a href="/privacy" target="_blank" rel="noopener noreferrer" className="text-emerald-300 underline underline-offset-2 hover:text-emerald-200">
            Privacy Policy
          </a>
        </p>
        <label className="mt-5 flex cursor-pointer items-start gap-3 rounded-lg border border-neutral-700 p-3 text-sm text-neutral-100 has-[:checked]:border-emerald-700/60">
          <input
            type="checkbox"
            checked={agreed}
            onChange={(e) => setAgreed(e.target.checked)}
            className="mt-0.5 h-4 w-4 shrink-0 accent-emerald-600"
          />
          <span>I agree to the Terms of Service and Privacy Policy</span>
        </label>
        {accept.isError && !(accept.error instanceof ApiError && accept.error.status === 409) && (
          <div className="mt-3">
            <ErrorBox title="Couldn't save that" detail={String(accept.error)} />
          </div>
        )}
        <button
          type="button"
          disabled={!agreed || accept.isPending}
          onClick={() => accept.mutate()}
          className="mt-4 w-full rounded-md bg-emerald-700 px-4 py-2 text-sm font-medium text-emerald-50 hover:bg-emerald-800 disabled:cursor-not-allowed disabled:bg-neutral-800 disabled:text-neutral-500"
        >
          {accept.isPending ? "Saving…" : "Continue"}
        </button>
      </main>
    </div>
  );
}
