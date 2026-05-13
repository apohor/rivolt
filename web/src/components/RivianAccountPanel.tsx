import { useState } from "react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { ErrorBox, Spinner } from "./ui";

// RivianAccountPanel drives the POST /api/settings/rivian/{login,mfa,
// logout} flow. Three UI states derived from the status endpoint:
//
//   - Not enabled   → read-only notice (RIVIAN_CLIENT=stub|mock).
//   - Not auth'd    → email + password form.
//   - MFA pending   → OTP form (email/password are already stashed in
//                     the server-side LiveClient).
//   - Authenticated → email + logout button.
//
// Credentials are never stored in React state longer than the request
// itself; the backend owns the bearer tokens.
export function RivianAccountPanel() {
  const qc = useQueryClient();
  const status = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    // Refresh when returning to the tab; a session may have expired.
    staleTime: 30_000,
  });

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [otp, setOtp] = useState("");

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["rivian"] });
    // After login/logout the vehicle list and any live state change;
    // kick off a refetch so LivePanel/LiveSummary catch up without a
    // page reload.
    qc.invalidateQueries({ queryKey: ["vehicles"] });
  };

  const login = useMutation({
    mutationFn: () => backend.rivianLogin(email, password),
    onSuccess: () => {
      setPassword(""); // drop cleartext from memory on success
      invalidate();
    },
  });
  const mfa = useMutation({
    mutationFn: () => backend.rivianMFA(otp),
    onSuccess: () => {
      setOtp("");
      setEmail("");
      setPassword("");
      invalidate();
    },
  });
  const logout = useMutation({
    mutationFn: () => backend.rivianLogout(),
    onSuccess: invalidate,
  });

  if (status.isLoading) return <Spinner />;
  if (status.isError) {
    return (
      <ErrorBox title="Couldn't load Rivian status" detail={String(status.error)} />
    );
  }
  const s = status.data;
  if (!s?.enabled) {
    return (
      <p className="text-sm text-neutral-400">
        Live Rivian client is disabled (
        <code className="text-neutral-300">RIVIAN_CLIENT=stub</code> or{" "}
        <code className="text-neutral-300">mock</code>). Restart the server
        without that env var — or set it to{" "}
        <code className="text-neutral-300">live</code> — to enable sign-in.
      </p>
    );
  }

  if (s.authenticated) {
    return (
      <div className="space-y-3">
        {s.needs_reauth && (
          <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
            <div className="font-medium text-amber-200">
              Rivian session expired
            </div>
            <p className="mt-1 text-xs text-amber-100/80">
              {s.needs_reauth_reason ||
                "Rivian rejected our stored session token. Drives and live state may stop recording until you re-sign in."}
            </p>
            <p className="mt-1 text-xs text-amber-100/60">
              Sign out below, then sign in again with your Rivian password (an
              OTP email will follow).
            </p>
          </div>
        )}
        <div className="flex items-center justify-between gap-3">
          <div className="text-sm">
            <div className="text-neutral-200">Connected as</div>
            <div className="text-xs text-neutral-500">{s.email || "unknown"}</div>
          </div>
          <button
            onClick={() => logout.mutate()}
            disabled={logout.isPending}
            className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-rose-500/50 hover:text-rose-300 disabled:opacity-50"
          >
            {logout.isPending ? "Signing out…" : "Sign out"}
          </button>
        </div>
      </div>
    );
  }

  if (s.mfa_pending) {
    return (
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (otp.trim().length < 4) return;
          mfa.mutate();
        }}
        className="space-y-2"
      >
        <p className="text-xs text-neutral-400">
          Rivian sent a one-time code to your email. Enter it to finish
          signing in.
        </p>
        <div className="flex gap-2">
          <input
            type="text"
            inputMode="numeric"
            pattern="[0-9]*"
            autoComplete="one-time-code"
            placeholder="123456"
            value={otp}
            onChange={(e) => setOtp(e.target.value.replace(/[^0-9]/g, ""))}
            className="flex-1 rounded-md border border-neutral-700 bg-neutral-950 px-3 py-2 text-sm tabular-nums text-neutral-200"
          />
          <button
            type="submit"
            disabled={mfa.isPending || otp.trim().length < 4}
            className="rounded-md bg-emerald-600/90 px-3 py-2 text-sm font-medium text-neutral-50 hover:bg-emerald-500 disabled:opacity-50"
          >
            {mfa.isPending ? "…" : "Verify"}
          </button>
          <button
            type="button"
            onClick={() => logout.mutate()}
            className="rounded-md border border-neutral-700 px-3 py-2 text-sm text-neutral-400 hover:text-neutral-200"
          >
            Cancel
          </button>
        </div>
        {mfa.isError && (
          <ErrorBox title="MFA failed" detail={String(mfa.error)} />
        )}
      </form>
    );
  }

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!email || !password) return;
        login.mutate();
      }}
      className="space-y-2"
    >
      <div className="rounded-md border border-emerald-900/60 bg-emerald-950/30 px-3 py-2 text-xs leading-relaxed text-emerald-200/90">
        <div className="mb-1 font-semibold text-emerald-200">
          Your credentials, your control
        </div>
        <ul className="space-y-0.5 text-emerald-200/80">
          <li>
            • Your <strong>password is never stored</strong> — it's sent once
            to Rivian to mint a session token, then dropped.
          </li>
          <li>
            • The session token is <strong>AES-GCM encrypted at rest</strong>{" "}
            with a per-install key, bound to your account so no other user
            can read it.
          </li>
          <li>
            • <strong>Disconnect any time</strong> via the Sign out button —
            the stored token is wiped immediately.
          </li>
        </ul>
      </div>
      <input
        type="email"
        autoComplete="username"
        required
        placeholder="you@example.com"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        className="w-full rounded-md border border-neutral-700 bg-neutral-950 px-3 py-2 text-sm text-neutral-200"
      />
      <input
        type="password"
        autoComplete="current-password"
        required
        placeholder="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        className="w-full rounded-md border border-neutral-700 bg-neutral-950 px-3 py-2 text-sm text-neutral-200"
      />
      <button
        type="submit"
        disabled={login.isPending || !email || !password}
        className="rounded-md bg-emerald-600/90 px-3 py-2 text-sm font-medium text-neutral-50 hover:bg-emerald-500 disabled:opacity-50"
      >
        {login.isPending ? "Signing in…" : "Sign in"}
      </button>
      {login.isError && (
        <ErrorBox title="Sign-in failed" detail={String(login.error)} />
      )}
    </form>
  );
}
