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
      <p className="text-xs text-neutral-400">
        Sign in with your Rivian Owner App credentials. Rivolt stores the
        session token locally; your password is sent once and never
        written to disk.
      </p>
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
