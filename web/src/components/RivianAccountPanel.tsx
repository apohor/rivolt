import { useState } from "react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { backend, type OwnedVehicle } from "../lib/api";
import { ErrorBox, Spinner, Toggle } from "./ui";
import { track } from "../lib/track";
import { expiryDate } from "../lib/rivianExpiry";
import { connectFailure, type ConnectFailure } from "../lib/connectErrors";

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
// The usual reasons a signed-in Rivian account shows no vehicle (growth's words), shared by every place that says so.
export const NO_VEHICLE_CAUSE =
  "Usual cause: a driver account whose invite isn't accepted yet. Sign in to the Rivian app on a phone with this account once, then tap";
export const NO_VEHICLE_MAIN_ACCOUNT = "Connected your main account instead? Make sure a vehicle shows in the Rivian app on it, then check again.";

// RivianExpiryEmailToggle: the heads-up emails before the connection expires (7 days and 1 day ahead). On by default; hidden when the
// server has no such setting. It leaves the reconnect email for a connection that has already failed alone.
function RivianExpiryEmailToggle() {
  const qc = useQueryClient();
  const setting = useQuery({
    queryKey: ["settings", "rivian-expiry-email"],
    queryFn: () => backend.rivianExpiryEmailGet(),
    retry: 0,
    staleTime: 60_000,
  });
  const put = useMutation({
    mutationFn: (enabled: boolean) => backend.rivianExpiryEmailPut(enabled),
    onSuccess: (data) => qc.setQueryData(["settings", "rivian-expiry-email"], data),
  });
  if (!setting.data) return null;
  return (
    <div data-rivian-expiry-email className="flex items-start justify-between gap-4">
      <div>
        <label htmlFor="rivian-expiry-email-toggle" className="text-sm text-neutral-100">
          Email me before the connection expires
        </label>
        <p className="mt-0.5 text-xs text-neutral-500">Sent 7 days and 1 day ahead.</p>
        {put.isError && (
          <p role="alert" className="mt-1 text-xs text-rose-400">
            Couldn&apos;t save. Try again in a moment.
          </p>
        )}
      </div>
      <Toggle id="rivian-expiry-email-toggle" checked={setting.data.enabled} disabled={put.isPending} onChange={(v) => put.mutate(v)} />
    </div>
  );
}

// `onLater` (onboarding) adds an "I'll do this later" step to a failure that is Rivian's, not the user's.
export function RivianAccountPanel({ onLater }: { onLater?: () => void } = {}) {
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
  // Recommended path first: a dedicated driver account keeps the main
  // login out of Rivolt and is revocable from the Rivian app. The main
  // account stays available.
  const [kind, setKind] = useState<"driver" | "main">("driver");
  const [whyOpen, setWhyOpen] = useState(false);

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["rivian"] });
    // After login/logout the vehicle list and any live state change;
    // kick off a refetch so LivePanel catches up without a
    // page reload.
    qc.invalidateQueries({ queryKey: ["vehicles"] });
  };

  const login = useMutation({
    mutationFn: () => {
      track("rivian_connect_start", { authorized_driver: kind === "driver" });
      return backend.rivianLogin(email, password);
    },
    onError: (e) => track("rivian_connect_fail", { class: connectFailure(e, "password").track }),
    onSuccess: () => {
      setPassword(""); // drop cleartext from memory on success
      invalidate();
    },
  });
  const mfa = useMutation({
    mutationFn: () => backend.rivianMFA(otp),
    onError: (e) => track("rivian_connect_fail", { class: connectFailure(e, "code").track }),
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
  const [confirmingSignOut, setConfirmingSignOut] = useState(false);
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
  // A refetch that fails while the panel already has an answer keeps showing that answer (the banner polls this same status every 15 s, and
  // a blip must not swap a form someone is typing in for an error box).
  if (status.isError && !status.data) {
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

  // The status is polled (every 15 s by the banner) and, with more than one server answering, can briefly disagree with itself. While
  // someone is typing in the sign-in form, an answer that says "connected" must not swap the form away from under them: it waits until
  // their own sign-in succeeds. Once it has, the connected view shows (and a stale "session expired" flag from before the sign-in is
  // not shown over a sign-in that just worked).
  const typing = (email !== "" || password !== "" || otp !== "") && !mfa.isSuccess;
  const holdForm = typing && !(login.isSuccess && s.authenticated && otp === "");
  const justSignedIn = login.isSuccess || mfa.isSuccess;

  if (s.authenticated && !holdForm) {
    return (
      <div className="space-y-3">
        {s.needs_reauth && !justSignedIn && (
          <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
            <div className="font-medium text-amber-200">
              Rivian session expired
            </div>
            <p className="mt-1 text-xs text-amber-100/80">
              {s.needs_reauth_reason ||
                "Rivian rejected our stored session token. Drives and live state may stop recording until you re-sign in."}
            </p>
            <p className="mt-1 text-xs text-amber-100/60">
              Sign out of Rivian below, then sign in again with your Rivian password (a
              one-time code will follow by email or in your authenticator app).
            </p>
          </div>
        )}
        <div className="flex items-center justify-between gap-3">
          <div className="text-sm">
            <div className="text-neutral-200">Connected as</div>
            <div className="text-xs text-neutral-500">{s.email || "unknown"}</div>
            {expiryDate(s.session_expires_at) && (
              <div data-rivian-expires className="text-xs text-neutral-500">Connected · expires {expiryDate(s.session_expires_at)}</div>
            )}
          </div>
          {!confirmingSignOut && (
            <button
              type="button"
              onClick={() => setConfirmingSignOut(true)}
              disabled={logout.isPending}
              className="rounded-md border border-rose-500/40 px-3 py-1.5 text-sm text-rose-300 hover:border-rose-500/70 hover:bg-rose-500/10 disabled:opacity-50"
            >
              {logout.isPending ? "Signing out…" : "Sign out of Rivian"}
            </button>
          )}
        </div>
        {confirmingSignOut && (
          <div data-rivian-signout-confirm role="alertdialog" aria-label="Sign out of Rivian?" className="rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
            <p className="font-medium text-rose-200">Sign out of Rivian?</p>
            <p className="mt-1 text-xs text-neutral-300">
              Rivolt deletes the stored Rivian session and stops recording until you sign in again. Your history stays.
            </p>
            <div className="mt-3 flex flex-wrap gap-2">
              <button
                type="button"
                onClick={() => {
                  setConfirmingSignOut(false);
                  logout.mutate();
                }}
                disabled={logout.isPending}
                className="rounded-md bg-rose-700 px-3 py-1.5 text-sm font-semibold text-white hover:bg-rose-600 disabled:opacity-50"
              >
                Yes, sign out of Rivian
              </button>
              <button type="button" onClick={() => setConfirmingSignOut(false)} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500">
                Cancel
              </button>
            </div>
          </div>
        )}
        <RivianExpiryEmailToggle />
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

  if (s.mfa_pending || (holdForm && login.isSuccess && !mfa.isSuccess)) {
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
        <div className="flex flex-wrap gap-2">
          <input
            type="text"
            inputMode="numeric"
            pattern="[0-9]*"
            autoComplete="one-time-code"
            placeholder="123456"
            value={otp}
            onChange={(e) => setOtp(e.target.value.replace(/[^0-9]/g, ""))}
            className="min-w-[8rem] flex-1 rounded-md border border-neutral-700 bg-neutral-950 px-3 py-2 text-sm tabular-nums text-neutral-200"
          />
          <button
            type="submit"
            disabled={mfa.isPending || otp.trim().length < 4}
            className="rounded-md bg-emerald-600/90 px-3 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
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
          <FailureBox
            f={connectFailure(mfa.error, "code")}
            primary={{ label: "Enter code again", run: () => { setOtp(""); mfa.reset(); } }}
            secondary={{ label: "Start again for a new code", run: () => logout.mutate() }}
          />
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
      <p className="text-sm text-neutral-300">
        We recommend a dedicated driver account, so Rivolt never holds your main Rivian login.
      </p>
      <div role="radiogroup" aria-label="Which account" className="space-y-1.5 text-sm">
        <label className="flex cursor-pointer items-start gap-2 rounded-md border border-neutral-800 p-2 has-[:checked]:border-emerald-700/60">
          <input
            type="radio"
            name="rivian-account-kind"
            checked={kind === "driver"}
            onChange={() => setKind("driver")}
            className="mt-0.5"
          />
          <span className="text-neutral-200">
            Use a dedicated driver account <span className="text-emerald-300">(recommended)</span>
          </span>
        </label>
        <label className="flex cursor-pointer items-start gap-2 rounded-md border border-neutral-800 p-2 has-[:checked]:border-neutral-600">
          <input
            type="radio"
            name="rivian-account-kind"
            checked={kind === "main"}
            onChange={() => setKind("main")}
            className="mt-0.5"
          />
          <span className="text-neutral-200">Use my main Rivian account</span>
        </label>
      </div>
      <button
        type="button"
        aria-expanded={whyOpen}
        onClick={() => setWhyOpen((v) => !v)}
        className="text-xs text-emerald-300 hover:underline"
      >
        {whyOpen ? "▾" : "▸"} Why a driver account?
      </button>
      {whyOpen && <WhyDriverAccount />}
      <div data-connect-reassure className="rounded-md border border-emerald-900/60 bg-emerald-950/30 px-3 py-2 text-xs leading-relaxed text-emerald-200/90">
        <div className="mb-1 font-semibold text-emerald-200">Connecting, in plain terms</div>
        <ul className="space-y-1 text-emerald-200/80">
          <li>
            • <strong>Safest:</strong> connect a separate, free Authorized Driver account instead of your main login.{" "}
            <a href={DRIVER_GUIDE} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-emerald-100">
              How to set one up
            </a>
            .
          </li>
          <li>
            • <strong>What we keep:</strong> your Rivian email and encrypted session tokens. Your password is never stored; it is used once to sign in to Rivian.
          </li>
          <li>
            • <strong>Read-only:</strong> Rivolt reads telemetry, drives and charging. It can&apos;t control your truck.
          </li>
          <li>
            • <strong>Undo any time:</strong> sign out of Rivian here in Settings (the stored session is deleted), or remove the driver in the Rivian app.
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
      <p className="text-xs text-neutral-500">
        {kind === "driver" ? (
          <>
            Use the email you added as an Authorized Driver. Haven&apos;t set one up?{" "}
            <a
              href={DRIVER_GUIDE}
              target="_blank"
              rel="noopener noreferrer"
              className="underline hover:text-neutral-300"
            >
              Show me how
            </a>
            .
          </>
        ) : (
          "You can switch to a driver account later."
        )}
      </p>
      <input
        type="password"
        autoComplete="current-password"
        required
        placeholder="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        className="w-full rounded-md border border-neutral-700 bg-neutral-950 px-3 py-2 text-sm text-neutral-200"
      />
      <p data-connect-privacy className="text-xs text-neutral-400">
        Rivolt records your vehicle's GPS location and other vehicle data to build your drives and charges. See the{" "}
        <a href="/privacy" target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-200">
          Privacy policy
        </a>
        .
      </p>
      <button
        type="submit"
        disabled={login.isPending || !email || !password}
        className="rounded-md bg-emerald-600/90 px-3 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
      >
        {login.isPending ? "Signing in…" : kind === "driver" ? "Sign in with driver account" : "Sign in"}
      </button>
      {kind === "driver" && (
        <p className="text-xs text-neutral-500">Recommended: a dedicated driver account.</p>
      )}
      {login.isError && (
        <FailureBox
          f={connectFailure(login.error, "password")}
          primary={{ label: "Try again", run: () => login.mutate() }}
          secondary={
            connectFailure(login.error, "password").cls === "user_action"
              ? { label: kind === "driver" ? "Use my main account instead" : "Use a driver account instead", run: () => { login.reset(); setKind(kind === "driver" ? "main" : "driver"); } }
              : onLater && ["transient", "outage"].includes(connectFailure(login.error, "password").cls)
                ? { label: "I'll do this later", run: onLater }
                : undefined
          }
        />
      )}
    </form>
  );
}

// A failed connect: the class-based message with a primary step and, where there is one, a secondary. Details stay filled in.
type Step = { label: string; run: () => void };
function FailureBox({ f, primary, secondary }: { f: ConnectFailure; primary: Step; secondary?: Step }) {
  return (
    <div role="alert" data-connect-failure={f.cls} className="rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
      <div className="font-medium text-rose-200">{f.title}</div>
      <p className="mt-1 text-xs leading-relaxed text-rose-100/80">{f.body}</p>
      <div className="mt-2 flex flex-wrap items-center gap-3">
        <button type="button" onClick={primary.run} className="rounded-md border border-rose-400/50 px-3 py-1.5 text-xs font-medium text-rose-100 hover:bg-rose-500/10">
          {primary.label}
        </button>
        {secondary && (
          <button type="button" onClick={secondary.run} className="text-xs text-emerald-300 underline underline-offset-2 hover:text-emerald-200">
            {secondary.label}
          </button>
        )}
      </div>
    </div>
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
        Sign-in worked, but no vehicle is on this account yet. {NO_VEHICLE_CAUSE}{" "}
        <strong>Check again</strong>.
      </p>
      <p className="mt-1 text-xs leading-relaxed text-amber-100/80">{NO_VEHICLE_MAIN_ACCOUNT}</p>
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

const DRIVER_GUIDE =
  "https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md#recommended-connect-a-dedicated-authorized-driver-account";

// WhyDriverAccount: the honest case for a driver account, trade-offs
// included (PM copy, ad-onboarding-copy.md). "Driver account" in the UI;
// "Authorized Driver" only in the docs.
function WhyDriverAccount() {
  return (
    <div className="space-y-2 rounded-md border border-neutral-800 bg-neutral-900/40 p-3 text-xs leading-relaxed text-neutral-300">
      <p>
        <strong className="text-neutral-100">It keeps your main login out of Rivolt.</strong> Rivolt stores a session
        token, not your password, but a separate account means a separate login to revoke.
      </p>
      <p>
        <strong className="text-neutral-100">You can remove Rivolt in one tap.</strong> Remove the driver in the
        Rivian app and Rivolt loses access.
      </p>
      <p>
        <strong className="text-neutral-100">It&apos;s gentler on your own Rivian app.</strong> Rivolt&apos;s requests
        count against the driver account, so they&apos;re less likely to slow down the app on your phone.
      </p>
      <p>
        <strong className="text-neutral-100">The honest trade-offs:</strong> you need a second email and a few
        minutes in the Rivian app, and a driver account may not see history from before it was added. Rivolt is
        unofficial; Rivian can change or block access at any time with either account.
      </p>
      <a href={DRIVER_GUIDE} target="_blank" rel="noopener noreferrer" className="inline-block text-emerald-300 hover:underline">
        Step-by-step guide
      </a>
    </div>
  );
}

// AuthorizedDriverNote used to be a low-key optional aside under the
// form. The sign-in form itself now recommends a driver account (with
// the "Why?" and the main-account option), so this renders nothing; the
// export stays so existing callers keep compiling.
export function AuthorizedDriverNote() {
  return null;
}
