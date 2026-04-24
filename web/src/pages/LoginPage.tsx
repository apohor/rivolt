import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { ApiError, backend } from "../lib/api";
import Logo from "../components/Logo";

// LoginPage is the cookie-issuing front door. It sits outside the
// AppLayout route tree on purpose — no header, no nav, no API
// requests firing on mount. A logged-in user hitting /login is
// bounced straight back to ?next= (or /), so bookmarking the login
// URL doesn't log you out.
export default function LoginPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const nextPath = params.get("next") || "/";

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  // If the session cookie is still valid (e.g. user opened /login by
  // accident, or the browser restored a tab), skip the form.
  useEffect(() => {
    let cancelled = false;
    backend.whoami().then((me) => {
      if (!cancelled && me) navigate(nextPath, { replace: true });
    });
    return () => {
      cancelled = true;
    };
  }, [navigate, nextPath]);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await backend.login(username, password);
      navigate(nextPath, { replace: true });
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        // Match the backend's deliberate ambiguity: one error for
        // both wrong-user and wrong-password so we don't become a
        // username oracle.
        setError("Invalid credentials");
      } else if (err instanceof ApiError && err.status === 503) {
        setError("Auth is not configured on the server");
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="min-h-full flex items-center justify-center px-4 py-10 app-safe-top">
      <form
        onSubmit={onSubmit}
        className="w-full max-w-sm rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg"
      >
        <div className="mb-6 flex items-center gap-2 text-neutral-100">
          <Logo size={24} className="text-emerald-400" />
          <span className="text-lg font-semibold tracking-tight">Rivolt</span>
        </div>
        <h1 className="mb-1 text-base font-semibold text-neutral-100">Sign in</h1>
        <p className="mb-5 text-sm text-neutral-400">
          Use the credentials configured in <code className="text-neutral-300">RIVOLT_USERNAME</code> /{" "}
          <code className="text-neutral-300">RIVOLT_PASSWORD</code>.
        </p>

        <label className="mb-3 block text-sm">
          <span className="mb-1 block text-neutral-300">Username</span>
          <input
            autoFocus
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="w-full rounded-md border border-neutral-800 bg-neutral-900 px-3 py-2 text-neutral-100 outline-none focus:border-emerald-600"
          />
        </label>
        <label className="mb-4 block text-sm">
          <span className="mb-1 block text-neutral-300">Password</span>
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full rounded-md border border-neutral-800 bg-neutral-900 px-3 py-2 text-neutral-100 outline-none focus:border-emerald-600"
          />
        </label>

        {error && (
          <div
            role="alert"
            className="mb-4 rounded-md border border-rose-900 bg-rose-950/50 px-3 py-2 text-sm text-rose-300"
          >
            {error}
          </div>
        )}

        <button
          type="submit"
          disabled={submitting || !username || !password}
          className="w-full rounded-md bg-emerald-700 px-3 py-2 text-sm font-medium text-white transition hover:bg-emerald-600 disabled:opacity-50"
        >
          {submitting ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
