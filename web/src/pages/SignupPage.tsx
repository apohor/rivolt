import { useState, useMemo, useEffect } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { backend, ApiError } from "../lib/api";
import Logo from "../components/Logo";

// Password complexity rules — must mirror backend validatePassword in
// internal/api/signup.go so the live checklist and the server agree.
const rules = [
  { id: "len", label: "At least 12 characters", test: (p: string) => p.length >= 12 },
  { id: "upper", label: "One uppercase letter", test: (p: string) => /[A-Z]/.test(p) },
  { id: "lower", label: "One lowercase letter", test: (p: string) => /[a-z]/.test(p) },
  { id: "digit", label: "One digit", test: (p: string) => /[0-9]/.test(p) },
  { id: "special", label: "One special character", test: (p: string) => /[^A-Za-z0-9]/.test(p) },
];

export default function SignupPage() {
  const navigate = useNavigate();
  const [search] = useSearchParams();
  const token = search.get("token") ?? "";

  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [created, setCreated] = useState(false);

  // Token-prefill state: when the URL carries ?token=…, we ask the
  // server to resolve it to an email so the user only fills password
  // + (optional) display name. tokenStatus: "checking" while the
  // lookup is in flight; "valid" after success; "invalid" on 410.
  const [tokenStatus, setTokenStatus] = useState<"none" | "checking" | "valid" | "invalid">(
    token ? "checking" : "none",
  );
  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    backend
      .signupTokenLookup(token)
      .then((res) => {
        if (cancelled) return;
        setEmail(res.email);
        setTokenStatus("valid");
      })
      .catch(() => {
        if (cancelled) return;
        setTokenStatus("invalid");
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  // Request-access form state. When the URL has no valid token this
  // is the page's primary content — anyone can leave their email and
  // we'll come back via email with a one-click signup link.
  const [reqEmail, setReqEmail] = useState("");
  const [reqError, setReqError] = useState<string | null>(null);
  const [reqSubmitting, setReqSubmitting] = useState(false);
  const [reqSent, setReqSent] = useState(false);

  const passwordChecks = useMemo(
    () => rules.map((r) => ({ ...r, ok: r.test(password) })),
    [password],
  );
  const passwordValid = passwordChecks.every((c) => c.ok);
  const passwordsMatch = password === confirmPassword && confirmPassword.length > 0;

  // Token-mode is the only mode now — the user got here via a magic
  // link from an admin approval. Without a valid token the page
  // shows the "request access" form instead of a signup form.
  const tokenMode = tokenStatus === "valid";
  const canSubmit =
    tokenMode &&
    email.trim().length > 0 &&
    passwordValid &&
    passwordsMatch &&
    !submitting;

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSubmit) return;
    setError(null);
    setSubmitting(true);
    try {
      await backend.signup({
        signup_token: token,
        display_name: displayName.trim() || undefined,
        password,
      });
      // Drop any existing session before showing the success screen so a
      // previously-logged-in user (e.g. admin testing signup) isn't
      // silently kept in their old account by the login page's whoami
      // short-circuit.
      try { await backend.logout(); } catch { /* no session is fine */ }
      setCreated(true);
    } catch (err) {
      if (err instanceof ApiError) {
        const msg = (err.body as { error?: string } | null)?.error;
        setError(msg ?? `Error ${err.status}`);
      } else {
        setError("Unexpected error. Please try again.");
      }
    } finally {
      setSubmitting(false);
    }
  }

  async function handleRequestSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (reqSubmitting) return;
    const trimmed = reqEmail.trim();
    if (!trimmed) {
      setReqError("Email is required");
      return;
    }
    setReqError(null);
    setReqSubmitting(true);
    try {
      await backend.requestSignup({
        email: trimmed,
      });
      // Backend returns ok=true for both fresh requests and
      // already-pending duplicates; either way the user-facing
      // outcome is identical so we don't differentiate.
      setReqSent(true);
    } catch (err) {
      if (err instanceof ApiError) {
        const msg = (err.body as { error?: string } | null)?.error;
        setReqError(msg ?? `Error ${err.status}`);
      } else {
        setReqError("Unexpected error. Please try again.");
      }
    } finally {
      setReqSubmitting(false);
    }
  }

  if (created) {
    return (
      <div className="min-h-full flex items-center justify-center px-4 py-10 app-safe-top">
        <div className="w-full max-w-sm rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg">
          <div className="mb-6 flex items-center gap-2 text-neutral-100">
            <Logo size={24} className="text-emerald-400" />
            <span className="text-lg font-semibold tracking-tight">Rivolt</span>
          </div>
          <h1 className="mb-3 text-base font-semibold text-neutral-100">Account created!</h1>
          <p className="mb-4 text-sm text-neutral-400">
            Your credentials are being provisioned. This takes up to a minute —
            if you sign in too quickly you may see an "incorrect password" error.
            Wait a moment, then click below.
          </p>
          <button
            type="button"
            onClick={() => navigate("/login?next=/onboarding", { replace: true })}
            className="w-full rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500"
          >
            Sign in to your new account
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-full flex items-center justify-center px-4 py-10 app-safe-top">
      <div className="w-full max-w-sm rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg">
        <div className="mb-6 flex items-center gap-2 text-neutral-100">
          <Logo size={24} className="text-emerald-400" />
          <span className="text-lg font-semibold tracking-tight">Rivolt</span>
        </div>

        <div className="mb-2 flex items-center gap-2">
          <h1 className="text-base font-semibold text-neutral-100">
            {tokenMode ? "Finish your signup" : "Request access"}
          </h1>
          <span className="rounded-full border border-amber-700/60 bg-amber-950/40 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-300">
            Beta
          </span>
        </div>

        {/* Token-state banner. Kept compact — the H1 above already
            tells the user what state they're in. */}
        {tokenStatus === "checking" && (
          <div className="mb-4 rounded-md border border-neutral-800 bg-neutral-900/60 px-3 py-2 text-xs text-neutral-300">
            Checking your signup link…
          </div>
        )}
        {tokenStatus === "invalid" && (
          <div className="mb-4 rounded-md border border-rose-900 bg-rose-950/40 px-3 py-2 text-xs text-rose-200">
            This signup link is invalid or has expired. Request a new one below.
          </div>
        )}
        {tokenStatus === "valid" && (
          <p className="mb-4 text-sm text-emerald-300">
            You're approved. Pick a password to finish — your email is already set.
          </p>
        )}

        {!tokenMode && tokenStatus !== "checking" && (
          <>
            <p className="mb-3 text-sm text-neutral-300">
              Rivolt is an open-source Rivian companion in closed beta.
              Drop your email below — you'll get a one-click signup link
              once approved.
            </p>
            <ul className="mb-5 space-y-1 text-xs text-neutral-500">
              <li>• Live telemetry from your truck</li>
              <li>• Every drive and charge against your own $/kWh</li>
              <li>• Road-trip planner with cost, weather, and efficiency analysis</li>
            </ul>

            {!reqSent ? (
              <form onSubmit={handleRequestSubmit} className="flex flex-col gap-4">
                <div>
                  <label className="mb-1.5 block text-xs font-medium text-neutral-400">
                    Email address
                  </label>
                  <input
                    type="email"
                    autoComplete="email"
                    required
                    placeholder="you@example.com"
                    value={reqEmail}
                    onChange={(e) => setReqEmail(e.target.value)}
                    className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm text-neutral-100 placeholder-neutral-600 focus:border-emerald-600 focus:outline-none"
                  />
                </div>
                {reqError && (
                  <div
                    role="alert"
                    className="rounded-md border border-rose-900 bg-rose-950/50 px-3 py-2 text-sm text-rose-300"
                  >
                    {reqError}
                  </div>
                )}
                <button
                  type="submit"
                  disabled={reqSubmitting || !reqEmail.trim()}
                  className="mt-1 w-full rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-40"
                >
                  {reqSubmitting ? "Sending…" : "Request access"}
                </button>
                <p className="text-center text-xs text-neutral-500">
                  Already have a signup link?{" "}
                  <span className="text-neutral-400">Open it from your email.</span>
                </p>
              </form>
            ) : (
              <div className="rounded-md border border-emerald-900 bg-emerald-950/40 px-3 py-4 text-sm text-emerald-200">
                <div className="mb-1 font-semibold">Request received.</div>
                <p className="text-emerald-200/80">
                  We'll email{" "}
                  <span className="text-emerald-100 font-mono">{reqEmail}</span>{" "}
                  when you're approved — usually within the same day.
                </p>
                <p className="mt-2 text-xs text-emerald-200/70">
                  Check your spam folder or add{" "}
                  <span className="text-emerald-100 font-mono">anton@rivolt.dev</span>{" "}
                  to your known recipients.
                </p>
              </div>
            )}
          </>
        )}

        {tokenStatus !== "checking" && (
          <div className="mt-5 rounded-md border border-neutral-800 bg-neutral-900/60 px-3 py-2 text-xs leading-relaxed text-neutral-400">
            <strong className="text-neutral-200">What's next:</strong> after
            you sign in, a short setup wizard helps you connect your Rivian
            account (a dedicated Authorized Driver login is the recommended
            pattern — see the{" "}
            <a
              href="https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md"
              target="_blank"
              rel="noopener noreferrer"
              className="text-emerald-500 hover:underline"
            >
              signup walkthrough
            </a>
            ) and lets you import past drives + charges from an ElectraFi
            CSV so your stats start with real history, not a blank slate.
          </div>
        )}

        {tokenMode && (
        <form onSubmit={handleSubmit} className="flex flex-col gap-4">

          {/* Email — read-only in token mode (server set it on approve) */}
          <div>
            <label className="mb-1.5 block text-xs font-medium text-neutral-400">
              Email address
            </label>
            <input
              type="email"
              autoComplete="email"
              placeholder="you@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              readOnly={tokenMode}
              className={`w-full rounded-md border border-neutral-700 px-3 py-2 text-sm placeholder-neutral-600 focus:outline-none ${
                tokenMode
                  ? "bg-neutral-900/60 text-neutral-400"
                  : "bg-neutral-900 text-neutral-100 focus:border-emerald-600"
              }`}
            />
          </div>

          {/* Display name (optional) */}
          <div>
            <label className="mb-1.5 block text-xs font-medium text-neutral-400">
              Display name{" "}
              <span className="text-neutral-600">(optional — defaults to email)</span>
            </label>
            <input
              type="text"
              autoComplete="name"
              placeholder="Alice"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-sm text-neutral-100 placeholder-neutral-600 focus:border-emerald-600 focus:outline-none"
            />
          </div>

          {/* Password */}
          <div>
            <label className="mb-1.5 block text-xs font-medium text-neutral-400">Password</label>
            <div className="relative">
              <input
                type={showPassword ? "text" : "password"}
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 pr-10 text-sm text-neutral-100 focus:border-emerald-600 focus:outline-none"
              />
              <button
                type="button"
                onClick={() => setShowPassword((v) => !v)}
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-xs text-neutral-500 hover:text-neutral-300"
              >
                {showPassword ? "hide" : "show"}
              </button>
            </div>
            {/* Live checklist */}
            {password.length > 0 && (
              <ul className="mt-2 space-y-1">
                {passwordChecks.map((c) => (
                  <li key={c.id} className="flex items-center gap-2 text-xs">
                    <span
                      className={c.ok ? "text-emerald-400" : "text-neutral-500"}
                    >
                      {c.ok ? "✓" : "○"}
                    </span>
                    <span className={c.ok ? "text-neutral-300" : "text-neutral-500"}>
                      {c.label}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          {/* Confirm password */}
          <div>
            <label className="mb-1.5 block text-xs font-medium text-neutral-400">
              Confirm password
            </label>
            <input
              type={showPassword ? "text" : "password"}
              autoComplete="new-password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              className={`w-full rounded-md border bg-neutral-900 px-3 py-2 text-sm text-neutral-100 focus:outline-none ${
                confirmPassword.length > 0 && !passwordsMatch
                  ? "border-rose-700 focus:border-rose-500"
                  : "border-neutral-700 focus:border-emerald-600"
              }`}
            />
            {confirmPassword.length > 0 && !passwordsMatch && (
              <p className="mt-1 text-xs text-rose-400">Passwords don't match</p>
            )}
          </div>

          {error && (
            <div
              role="alert"
              className="rounded-md border border-rose-900 bg-rose-950/50 px-3 py-2 text-sm text-rose-300"
            >
              {error}
            </div>
          )}

          <button
            type="submit"
            disabled={!canSubmit}
            className="mt-1 w-full rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-40"
          >
            {submitting ? "Creating account…" : "Create account"}
          </button>
        </form>
        )}

        <p className="mt-4 text-center text-xs text-neutral-600">
          Already have an account?{" "}
          <a href="/login" className="text-emerald-500 hover:underline">
            Sign in
          </a>
        </p>
      </div>
    </div>
  );
}
