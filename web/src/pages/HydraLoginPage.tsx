import { useEffect, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { ApiError, backend } from "../lib/api";
import Logo from "../components/Logo";

// HydraLoginPage handles the browser side of an OIDC code flow when
// a third-party app (Grafana, ArgoCD, …) hands the user off to Hydra
// and Hydra redirects them here with ?login_challenge=…. We render a
// password form that POSTs to Rivolt's backend, which authenticates
// against Kratos and tells Hydra to accept the login. The server's
// JSON response carries the next URL the browser must visit so the
// OAuth2 code grant can complete.
//
// This page deliberately mirrors LoginPage's shell — same logo,
// same card chrome — but does not share state with it: the cookie
// that LoginPage establishes is unrelated to the Kratos session
// this flow plants.
type LoginMeta = {
  challenge: string;
  client_id: string;
  client_name: string;
  requested_scope: string[];
  login_hint?: string;
};

// Hydra "skip" responses bypass the form: a remembered prior login
// means we accepted server-side and the SPA just navigates onward.
function isSkip(
  r: { skip?: boolean; redirect_to?: string },
): r is { skip: true; redirect_to: string } {
  return r.skip === true && typeof r.redirect_to === "string" && r.redirect_to.length > 0;
}

export default function HydraLoginPage() {
  const [params] = useSearchParams();
  const challenge = params.get("login_challenge") ?? "";

  const [meta, setMeta] = useState<LoginMeta | null>(null);
  const [metaError, setMetaError] = useState<string | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    if (!challenge) {
      setMetaError("Missing login_challenge in URL.");
      return;
    }
    let cancelled = false;
    backend
      .hydraLoginGet(challenge)
      .then((m) => {
        if (cancelled) return;
        if (isSkip(m)) {
          window.location.assign(m.redirect_to);
          return;
        }
        if (!m.challenge || !m.client_id) {
          setMetaError("Login challenge response was malformed.");
          return;
        }
        setMeta({
          challenge: m.challenge,
          client_id: m.client_id,
          client_name: m.client_name ?? m.client_id,
          requested_scope: m.requested_scope ?? [],
          login_hint: m.login_hint,
        });
        if (m.login_hint) setEmail(m.login_hint);
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        if (e instanceof ApiError) {
          setMetaError(`Login challenge could not be loaded (${e.status}).`);
        } else {
          setMetaError("Login challenge could not be loaded.");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [challenge]);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!meta || submitting) return;
    setSubmitting(true);
    setFormError(null);
    try {
      const out = await backend.hydraLoginPost({
        challenge: meta.challenge,
        email: email.trim(),
        password,
      });
      // Full-page navigation: redirect_to is on Hydra's host and
      // sets a cookie there. A fetch follow would never see it.
      window.location.assign(out.redirect_to);
    } catch (err: unknown) {
      if (err instanceof ApiError) {
        if (err.status === 401) {
          setFormError("Invalid email or password.");
        } else if (err.status === 400) {
          setFormError("Please fill in both email and password.");
        } else {
          setFormError(`Login failed (${err.status}).`);
        }
      } else {
        setFormError("Login failed. Please try again.");
      }
      setSubmitting(false);
    }
  }

  const ready = meta !== null && metaError === null;

  return (
    <div className="min-h-full flex items-center justify-center px-4 py-10 app-safe-top">
      <div className="w-full max-w-sm rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg">
        <div className="mb-6 flex items-center gap-2 text-neutral-100">
          <Logo size={24} className="text-emerald-400" />
          <span className="text-lg font-semibold tracking-tight">Rivolt</span>
        </div>
        <h1 className="mb-1 text-base font-semibold text-neutral-100">
          Sign in to continue
        </h1>
        {meta && (
          <p className="mb-5 text-sm text-neutral-400">
            <span className="text-neutral-200">{meta.client_name}</span> is
            requesting access to your Rivolt account.
          </p>
        )}

        {metaError && (
          <div
            role="alert"
            className="rounded-md border border-rose-900 bg-rose-950/50 px-3 py-2 text-sm text-rose-300"
          >
            {metaError}
          </div>
        )}

        {!metaError && !meta && (
          <p className="text-sm text-neutral-500">Loading…</p>
        )}

        {ready && (
          <form onSubmit={onSubmit} className="flex flex-col gap-3">
            <label className="flex flex-col gap-1 text-sm text-neutral-300">
              Email
              <input
                type="email"
                autoComplete="username"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="w-full rounded-md border border-neutral-800 bg-neutral-900 px-3 py-2 text-sm text-neutral-100 outline-none focus:border-emerald-700"
              />
            </label>
            <label className="flex flex-col gap-1 text-sm text-neutral-300">
              Password
              <input
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full rounded-md border border-neutral-800 bg-neutral-900 px-3 py-2 text-sm text-neutral-100 outline-none focus:border-emerald-700"
              />
            </label>

            {formError && (
              <div
                role="alert"
                className="rounded-md border border-rose-900 bg-rose-950/50 px-3 py-2 text-sm text-rose-300"
              >
                {formError}
              </div>
            )}

            <button
              type="submit"
              disabled={submitting}
              className="mt-1 w-full rounded-md border border-emerald-700 bg-emerald-700 px-3 py-2 text-sm font-medium text-neutral-50 transition hover:bg-emerald-600 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {submitting ? "Signing in…" : "Sign in"}
            </button>
          </form>
        )}

        {ready && meta.requested_scope.length > 0 && (
          <p className="mt-4 text-xs text-neutral-600">
            Scopes: {meta.requested_scope.join(", ")}
          </p>
        )}
      </div>
    </div>
  );
}
