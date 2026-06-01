import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { backend, type OIDCProvider } from "../lib/api";
import Logo from "../components/Logo";

// LoginPage is the cookie-issuing front door. It sits outside the
// AppLayout route tree on purpose — no header, no nav, no API
// requests firing on mount. A logged-in user hitting /login is
// bounced straight back to ?next= (or /), so bookmarking the login
// URL doesn't log you out.
// signInErrorMessage maps the stable ?error code the OIDC callback
// redirects with to user-facing copy. An unknown code falls back to
// the server-supplied ?error_description, then to a generic line, so
// a new backend code degrades gracefully without a frontend deploy.
function signInErrorMessage(code: string, description: string): string {
  switch (code) {
    case "session_expired":
      return "Your sign-in session expired. Please try again.";
    case "idp_error":
      return description || "Your identity provider reported an error.";
    case "provider_error":
      return "Couldn't complete sign-in with the provider. Please try again.";
    case "verification_failed":
      return "We couldn't verify your sign-in. Please try again.";
    case "account_disabled":
      return "This account has been disabled. Contact the administrator if you think this is a mistake.";
    case "server_error":
      return "Something went wrong on our end. Please try again.";
    default:
      return description || "Sign-in failed. Please try again.";
  }
}

export default function LoginPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const nextPath = params.get("next") || "/";

  // The OIDC callback redirects here with ?error + ?error_description
  // on any sign-in failure (expired state, IdP error, disabled
  // account, ...). Surface it as a banner above the provider buttons
  // rather than the bare white http.Error page the callback used to
  // render.
  const errorCode = params.get("error") ?? "";
  const errorDescription = params.get("error_description") ?? "";
  const signInError = errorCode
    ? signInErrorMessage(errorCode, errorDescription)
    : "";

  // OIDC providers, fetched once on mount. Empty array means the
  // server isn't configured for any provider — in OIDC-only mode
  // there is no password fallback, so we render a clear message
  // instead of a sign-in button.
  const [providers, setProviders] = useState<OIDCProvider[] | null>(null);

  // If the session cookie is still valid (e.g. user opened /login by
  // accident, or the browser restored a tab), skip straight to next.
  useEffect(() => {
    let cancelled = false;
    backend.whoami().then((me) => {
      if (!cancelled && me) navigate(nextPath, { replace: true });
    });
    backend.oidcProviders().then((p) => {
      if (!cancelled) setProviders(p);
    });
    return () => {
      cancelled = true;
    };
  }, [navigate, nextPath]);

  // OIDC start URLs are GET endpoints that 302 to the IdP; we
  // intentionally use full-page navigation rather than fetch +
  // hand-rolled redirect handling, because the browser must own
  // the cookie + Set-Cookie roundtrip.
  function startOIDC(p: OIDCProvider) {
    const url = new URL(p.start_url, window.location.origin);
    if (nextPath && nextPath !== "/") {
      url.searchParams.set("return", nextPath);
    }
    window.location.assign(url.toString());
  }

  return (
    <div className="min-h-full flex items-center justify-center px-4 py-10 app-safe-top">
      <div className="w-full max-w-sm rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg">
        <div className="mb-4 flex items-center gap-2 text-neutral-100">
          <Logo size={24} className="text-emerald-400" />
          <span className="text-lg font-semibold tracking-tight">Rivolt</span>
        </div>
        <div className="mb-2 flex items-center gap-2">
          <h1 className="text-base font-semibold text-neutral-100">Sign in</h1>
          <span className="rounded-full border border-amber-700/60 bg-amber-950/40 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-300">
            Beta
          </span>
        </div>
        <p className="mb-3 text-sm text-neutral-400">
          Your Rivian companion. After signing in you'll have:
        </p>
        <ul className="mb-4 space-y-1 text-xs text-neutral-500">
          <li>• Live telemetry from your truck</li>
          <li>• Every drive and charge tracked against your own $/kWh</li>
          <li>• Road-trip planner with real cost, weather, and efficiency analysis</li>
        </ul>
        {signInError && (
          <div
            role="alert"
            className="mb-4 rounded-md border border-rose-900 bg-rose-950/50 px-3 py-2 text-sm text-rose-300"
          >
            {signInError}
          </div>
        )}

        {providers && providers.length > 1 && (
          <p className="mb-4 text-xs text-neutral-500">
            Choose an identity provider to continue.
          </p>
        )}

        {providers === null && (
          <p className="text-sm text-neutral-500">Loading…</p>
        )}

        {providers !== null && providers.length === 0 && (
          <div
            role="alert"
            className="rounded-md border border-rose-900 bg-rose-950/50 px-3 py-2 text-sm text-rose-300"
          >
            No identity providers are configured on the server. Set
            <code className="mx-1 text-neutral-300">RIVOLT_OIDC_PROVIDERS</code>
            and the per-provider issuer / client credentials, then restart.
          </div>
        )}

        {providers !== null && providers.length > 0 && (
          <div className="flex flex-col gap-2">
            {providers.map((p) => (
              <button
                key={p.name}
                type="button"
                onClick={() => startOIDC(p)}
                className="w-full rounded-md border border-neutral-800 bg-neutral-900 px-3 py-2 text-sm font-medium text-neutral-100 transition hover:border-emerald-700 hover:bg-neutral-850"
              >
                Continue with {p.display_name}
              </button>
            ))}
          </div>
        )}

        <p className="mt-5 text-center text-xs text-neutral-600">
          New here?{" "}
          <a href="/signup" className="text-emerald-500 hover:underline">
            Create an account
          </a>
        </p>
      </div>
    </div>
  );
}
