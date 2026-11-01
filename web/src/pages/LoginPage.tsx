import { Fragment, useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { backend, type OIDCProvider } from "../lib/api";
import { ProviderButton, isApple } from "../components/ProviderButton";
import { APPLE_LINK_COPY } from "../content/appleLinkCopy";
import Logo from "../components/Logo";
import { useAdminOnly, useSignupEnabled } from "../lib/config";
import { SESSION_EXPIRED_TEXT, providerToRestart, readSigninProvider, rememberSigninProvider } from "../lib/signinDraft";

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
      return SESSION_EXPIRED_TEXT;
    case "idp_error":
      return description || "Your identity provider reported an error.";
    case "provider_error":
      return "Couldn't complete sign-in with the provider. Please try again.";
    case "verification_failed":
      return "We couldn't verify your sign-in. Please try again.";
    case "signups_disabled":
      return "Sign-ups are closed on this server. Ask the administrator for an account.";
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
  const queryClient = useQueryClient();
  const [params] = useSearchParams();
  const nextPath = params.get("next") || "/";
  // null until /api/config loads; the "Create an account" link only
  // renders once the server has confirmed sign-ups are open.
  const signupEnabled = useSignupEnabled();
  // The admin site gets a plain card: no product pitch, no sign-up, no links to pages it doesn't
  // serve. While /api/config is still loading the consumer copy is held back too, so it never
  // flashes up on the admin site.
  const adminOnly = useAdminOnly();
  const plain = adminOnly.loading || adminOnly.only;

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
    backend.whoami().then(async (me) => {
      if (cancelled || !me) return;
      // The app's cached "who am I" was taken signed out: refresh it so the terms gate applies.
      await queryClient.resetQueries({ queryKey: ["auth", "me"] });
      if (!cancelled) navigate(nextPath, { replace: true });
    });
    backend.oidcProviders().then((p) => {
      if (!cancelled) setProviders(p);
    });
    return () => {
      cancelled = true;
    };
  }, [navigate, nextPath, queryClient]);

  // OIDC start URLs are GET endpoints that 302 to the IdP; we
  // intentionally use full-page navigation rather than fetch +
  // hand-rolled redirect handling, because the browser must own
  // the cookie + Set-Cookie roundtrip.
  function startOIDC(p: OIDCProvider) {
    rememberSigninProvider(p.name);
    const url = new URL(p.start_url, window.location.origin);
    if (nextPath && nextPath !== "/") {
      url.searchParams.set("return", nextPath);
    }
    window.location.assign(url.toString());
  }

  // "Start sign-in again": the provider chosen last, started afresh (the email typed on its form is kept for the new form); with none
  // to repeat, the page without the error, to choose again.
  function restartSignin() {
    const again = providerToRestart(providers, readSigninProvider());
    if (again) {
      startOIDC(again);
      return;
    }
    navigate(nextPath === "/" ? "/login" : `/login?next=${encodeURIComponent(nextPath)}`, { replace: true });
  }

  return (
    <div className="min-h-full flex items-center justify-center px-4 py-10 app-safe-top">
      <div className="w-full max-w-sm rounded-xl border border-neutral-800 bg-neutral-950 p-6 shadow-lg">
        <div className="mb-4 flex items-center gap-2 text-neutral-100">
          <Logo size={24} className="text-emerald-400" />
          <span className="text-lg font-semibold tracking-tight">Rivolt</span>
        </div>
        {plain ? (
          <h1 data-admin-login={adminOnly.only ? "" : undefined} className="mb-4 text-base font-semibold text-neutral-100">
            {adminOnly.only ? "Sign in to Rivolt Admin" : "Sign in"}
          </h1>
        ) : (
          <>
            <div className="mb-2 flex items-center gap-2">
              <h1 className="text-base font-semibold text-neutral-100">Sign in</h1>
              <span className="rounded-full border border-amber-700/60 bg-amber-950/40 px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide text-amber-300">
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
          </>
        )}
        {signInError && (
          <div
            role="alert"
            className="mb-4 rounded-md border border-rose-900 bg-rose-950/50 px-3 py-2 text-sm text-rose-300"
          >
            {signInError}
            {errorCode === "session_expired" && (
              <div className="mt-2">
                <button
                  type="button"
                  data-restart-signin
                  onClick={restartSignin}
                  className="rounded-md border border-rose-800 px-3 py-1.5 text-sm font-medium text-rose-100 hover:bg-rose-900/40"
                >
                  Start sign-in again
                </button>
              </div>
            )}
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
          <>
            <div className="flex flex-col gap-2">
              {providers.map((p) => (
                <Fragment key={p.name}>
                  <ProviderButton provider={p} onClick={() => startOIDC(p)} />
                  {isApple(p) && (
                    <p data-apple-note className="text-[11px] leading-relaxed text-neutral-500">
                      {APPLE_LINK_COPY.signInNote}
                    </p>
                  )}
                </Fragment>
              ))}
            </div>
            {providers.length > 1 && !plain && (
              <p className="mt-3 text-[11px] leading-relaxed text-neutral-600">
                Accounts are keyed by email - use the same provider and email
                you signed up with, or a different address lands you in a
                separate, empty account.
              </p>
            )}
          </>
        )}

        {!plain && signupEnabled === true && (
          <p className="mt-5 text-center text-xs text-neutral-600">
            New here?{" "}
            <a href="/signup" className="text-emerald-500 hover:underline">
              Create an account
            </a>
          </p>
        )}

        {!plain && <p className="mt-4 text-center text-xs text-neutral-400">
          <a href="/privacy" className="underline underline-offset-2 hover:text-neutral-200">
            Privacy policy
          </a>
          {" · "}
          <a href="/terms" className="underline underline-offset-2 hover:text-neutral-200">
            Terms
          </a>
        </p>}
      </div>
    </div>
  );
}
