import { useState } from "react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { backend, type OwnedVehicle } from "../lib/api";
import { ErrorBox, Spinner } from "./ui";

// RivianAccountPanel drives the POST /api/settings/rivian/{login,mfa,
// logout} flow. Three UI states derived from the status endpoint:
//
//   - Not enabled   → read-only notice (RIVIAN_CLIENT=stub|mock).
//   - Not auth'd    → email + password form.
//   - MFA pending   → OTP form (email/password are already stashed in
//                     the server-side LiveClient).
//   - Authenticated → email + logout button, plus the vehicles the
//                     account exposes. Zero vehicles gets a warning and
//                     a "Check again" button: the sign-in worked but
//                     nothing will record (usually an Authorized Driver
//                     invite that hasn't been accepted yet).
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
  const authenticated = !!status.data?.authenticated;
  // DB-backed list, filled by the server during sign-in, so it's
  // current as soon as the login mutation invalidates it.
  const owned = useQuery({
    queryKey: ["vehicles", "owned"],
    queryFn: () => backend.listOwnedVehicles(),
    enabled: authenticated,
  });
  const recheck = useMutation({
    mutationFn: () => backend.rivianRefreshVehicles(),
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
              Sign out below, then sign in again with your Rivian password (a
              one-time code will follow by email or in your authenticator app).
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
        <ConnectedVehicles
          vehicles={owned.data?.vehicles}
          loading={owned.isLoading}
          rechecking={recheck.isPending}
          onRecheck={() => recheck.mutate()}
          recheckError={recheck.isError ? String(recheck.error) : null}
        />
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
          Enter the one-time code to finish signing in — check your email or
          authenticator app, depending on how 2FA is set up on your Rivian
          account.
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
            • <strong>Read-only</strong> — Rivolt reads telemetry, drives,
            and charging. It never sends commands to your vehicle.
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

// ConnectedVehicles confirms what a successful sign-in actually found.
// "Connected as …" alone read as done even when the Rivian account had
// no vehicles, and those users never learned why nothing recorded.
function ConnectedVehicles({
  vehicles,
  loading,
  rechecking,
  onRecheck,
  recheckError,
}: {
  vehicles: OwnedVehicle[] | undefined;
  loading: boolean;
  rechecking: boolean;
  onRecheck: () => void;
  recheckError: string | null;
}) {
  if (loading || !vehicles) return null;
  if (vehicles.length > 0) {
    return (
      <ul className="space-y-1 text-sm">
        {vehicles.map((v) => (
          <li key={v.id} className="flex items-center gap-2 text-neutral-300">
            <span className="text-emerald-400" aria-hidden>
              ✓
            </span>
            <span>{vehicleLabel(v)}</span>
          </li>
        ))}
      </ul>
    );
  }
  return (
    <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
      <div className="font-medium text-amber-200">
        No vehicles on this Rivian account
      </div>
      <p className="mt-1 text-xs leading-relaxed text-amber-100/80">
        The sign-in worked, but Rivian doesn't list any vehicle for it, so
        there's nothing to record yet. If this is an Authorized Driver
        account, accept the invite and sign in to the Rivian app once with
        it — then check again. Otherwise, sign out and connect the account
        that owns the vehicle.
      </p>
      <button
        type="button"
        onClick={onRecheck}
        disabled={rechecking}
        className="mt-2 rounded-md border border-amber-500/50 px-3 py-1.5 text-xs font-medium text-amber-100 hover:bg-amber-500/10 disabled:opacity-50"
      >
        {rechecking ? "Checking…" : "Check again"}
      </button>
      {recheckError && (
        <p className="mt-2 text-xs text-rose-300">{recheckError}</p>
      )}
    </div>
  );
}

function vehicleLabel(v: OwnedVehicle): string {
  const model = [v.model_year, v.model].filter(Boolean).join(" ");
  if (v.display_name && model) return `${model} “${v.display_name}”`;
  return v.display_name || model || v.vin || "Your Rivian";
}

// AuthorizedDriverNote is the optional, low-key alternative to
// connecting the primary Rivian login. It used to be the headline
// "Recommended" path, and asking a brand-new user to create a second
// Rivian account first was where most signups gave up.
export function AuthorizedDriverNote() {
  return (
    <p className="text-xs leading-relaxed text-neutral-500">
      <span className="text-neutral-400">Optional:</span> prefer to keep your
      main login out of it? Add a second Rivian account as an{" "}
      <a
        href="https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md#optional-dedicated-authorized-driver-account"
        target="_blank"
        rel="noopener noreferrer"
        className="underline hover:text-neutral-300"
      >
        Authorized Driver
      </a>{" "}
      and connect that instead. It only sees your vehicle once it has
      accepted the invite and signed in to the Rivian app.
    </p>
  );
}
