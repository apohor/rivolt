import { useEffect, useRef } from "react";
import { Link } from "react-router-dom";
import { useDemoEnabled } from "../lib/config";
import { connectFailure } from "../lib/connectErrors";

// The Rivian sign-in step under design key signup_v2: one screen, the assurances (each one true: the Rivian client sends only the sign-in
// itself, Login/LoginWithOTP, never a command; the password is used once; what is kept is the Rivian email and an encrypted session
// token), the safer option BEFORE the password, labelled fields so a password manager offers the Rivian login, and a way forward from
// every failure. The panel (RivianAccountPanel) owns the requests and the state; these are the two forms.
const link = "text-emerald-300 underline underline-offset-2 hover:text-emerald-200";
const input = "w-full rounded-md border border-neutral-700 bg-neutral-950 px-3 py-2 text-sm text-neutral-200";
const primaryBtn = "w-full rounded-md bg-emerald-600/90 px-3 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50";
const DEMO_HOST = "https://demo.rivolt.dev";

function useHeadingFocus() {
  const ref = useRef<HTMLHeadingElement>(null);
  useEffect(() => ref.current?.focus(), []);
  return ref;
}

function SampleLink() {
  const demoEnabled = useDemoEnabled();
  const hosted = /(^|\.)rivolt\.dev$/.test(window.location.hostname);
  if (!demoEnabled && !hosted) return null;
  return (
    <a href={demoEnabled ? "/demo?from=app" : `${DEMO_HOST}/demo?from=app`} className={link}>
      look at the sample truck
    </a>
  );
}

function Failure({ title, children, action }: { title: string; children: React.ReactNode; action?: { label: string; run: () => void } }) {
  return (
    <div role="alert" className="rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
      <div className="font-medium text-rose-200">{title}</div>
      <p className="mt-1 text-xs leading-relaxed text-rose-100/80">{children}</p>
      {action && (
        <button type="button" onClick={action.run} className="mt-2 rounded-md border border-rose-400/50 px-3 py-1.5 text-xs font-medium text-rose-100 hover:bg-rose-500/10">
          {action.label}
        </button>
      )}
    </div>
  );
}

// What went wrong, in the mock's words, with the step to take. `retry` runs the same request again.
function SignInFailure({ error, step, retry, onLater, onNewCode }: { error: unknown; step: "password" | "code"; retry: () => void; onLater?: () => void; onNewCode?: () => void }) {
  const f = connectFailure(error, step);
  if (f.cls === "rate_limited") return <Failure title="Too many tries right now">Sign-in is paused for about 10 minutes. Then try once more.</Failure>;
  if (f.cls === "transient" || f.cls === "outage") {
    return (
      <Failure title="Rivian isn't answering right now" action={{ label: "Try again", run: retry }}>
        Your details are fine. Rivian's sign-in is slow or down: try again in a few minutes, or <SampleLink />
        {onLater && (
          <>
            {" "}
            or{" "}
            <button type="button" onClick={onLater} className={link}>
              do this later
            </button>
          </>
        )}
        .
      </Failure>
    );
  }
  if (step === "code") {
    return (
      <Failure title="That code didn't work" action={onNewCode ? { label: "Get a new code", run: onNewCode } : undefined}>
        Codes expire after a few minutes. Enter the newest one, or get a new code: you'll enter your password again and Rivian sends a new one.
      </Failure>
    );
  }
  if (f.cls === "user_action") {
    return (
      <Failure title="Rivian didn't accept that email or password" action={{ label: "Try again", run: retry }}>
        Use the sign-in from the Rivian app. A driver account must accept its invite and set a password in the Rivian app first. Forgot it? Reset it in the Rivian app.
      </Failure>
    );
  }
  return (
    <Failure title="Couldn't sign in" action={{ label: "Try again", run: retry }}>
      Something went wrong on the way to Rivian. Try again in a few minutes.
    </Failure>
  );
}

export type LoginFormProps = {
  email: string;
  setEmail: (v: string) => void;
  password: string;
  setPassword: (v: string) => void;
  driver: boolean;
  setDriver: (v: boolean) => void;
  pending: boolean;
  error: unknown;
  onSubmit: () => void;
  onLater?: () => void;
  // The step heading: on the onboarding screen it is the page's h2, elsewhere the form has none.
  heading?: boolean;
};

export function RivianLoginV2({ email, setEmail, password, setPassword, driver, setDriver, pending, error, onSubmit, onLater, heading = true }: LoginFormProps) {
  const h = useHeadingFocus();
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (email && password) onSubmit();
      }}
      className="space-y-3 text-sm text-neutral-300"
    >
      {heading && (
        <h2 ref={h} tabIndex={-1} className="text-base font-semibold text-neutral-100 outline-hidden">
          Sign in to Rivian
        </h2>
      )}
      <p>Rivolt reads your truck's data through your Rivian account, so it needs the sign-in you use in the Rivian app.</p>
      <ul className="space-y-1.5 text-neutral-200">
        <li>✓ Read-only: Rivolt never sends commands to your truck.</li>
        <li>✓ Your password is used once and not stored. Rivolt keeps your Rivian email and an encrypted session token.</li>
        <li>✓ Disconnect any time in Settings.</li>
      </ul>
      <p data-connect-safer className="rounded-md border border-emerald-900/60 bg-emerald-950/30 px-3 py-2 text-xs leading-relaxed text-emerald-100/90">
        <strong className="text-emerald-100">Safer option:</strong> connect a separate Authorized Driver account instead of your own login, so you can cut Rivolt off from the Rivian app.{" "}
        <Link to="/driver-account" target="_blank" className={link}>
          How to set one up
        </Link>{" "}
        (a few minutes in the Rivian app).
      </p>
      <div>
        <label htmlFor="rivian-email" className="mb-1 block text-xs font-medium text-neutral-400">
          Rivian email
        </label>
        <input id="rivian-email" name="rivian-email" type="email" autoComplete="username" required placeholder="you@example.com" value={email} onChange={(e) => setEmail(e.target.value)} className={input} />
      </div>
      <label className="flex cursor-pointer items-center gap-2 text-xs text-neutral-300">
        <input type="checkbox" checked={driver} onChange={(e) => setDriver(e.target.checked)} className="h-4 w-4 accent-emerald-600" />
        This is an Authorized Driver account
      </label>
      <div>
        <label htmlFor="rivian-password" className="mb-1 block text-xs font-medium text-neutral-400">
          Rivian password
        </label>
        <input id="rivian-password" name="rivian-password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} className={input} />
      </div>
      <p data-connect-privacy className="text-xs text-neutral-400">
        Rivolt records your vehicle's GPS location and other vehicle data to build your drives and charges. See the{" "}
        <a href="/privacy" target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-neutral-200">
          Privacy policy
        </a>
        .
      </p>
      <button type="submit" disabled={pending || !email || !password} className={primaryBtn}>
        {pending ? "Signing in…" : "Sign in to Rivian"}
      </button>
      {!!error && <SignInFailure error={error} step="password" retry={onSubmit} onLater={onLater} />}
      <p className="text-xs text-neutral-400">
        Rivolt is unofficial and not affiliated with Rivian; Rivian can change or block access at any time.{" "}
        <Link to="/safe-to-connect" target="_blank" className={link}>
          Is it safe to connect?
        </Link>
      </p>
    </form>
  );
}

export type CodeFormProps = {
  otp: string;
  setOtp: (v: string) => void;
  pending: boolean;
  error: unknown;
  onSubmit: () => void;
  // Back to the sign-in form for a new code (the password is not kept), and out to a different account.
  onNewCode: () => void;
  onDifferent: () => void;
  heading?: boolean;
};

export function RivianCodeV2({ otp, setOtp, pending, error, onSubmit, onNewCode, onDifferent, heading = true }: CodeFormProps) {
  const h = useHeadingFocus();
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (otp.trim().length >= 4) onSubmit();
      }}
      className="space-y-3 text-sm text-neutral-300"
    >
      {heading && (
        <h2 ref={h} tabIndex={-1} className="text-base font-semibold text-neutral-100 outline-hidden">
          Enter the code from Rivian
        </h2>
      )}
      <p>
        Your Rivian account asks for a second step. Rivian sends a one-time code by email, or you find it in your authenticator app, depending on how your Rivian account is set up. Codes expire after a few minutes.
      </p>
      <div>
        <label htmlFor="rivian-code" className="mb-1 block text-xs font-medium text-neutral-400">
          Code
        </label>
        <input
          id="rivian-code"
          name="rivian-code"
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          autoComplete="one-time-code"
          value={otp}
          onChange={(e) => setOtp(e.target.value.replace(/[^0-9]/g, ""))}
          className={`${input} text-center tracking-[0.5em] tabular-nums`}
        />
      </div>
      <button type="submit" disabled={pending || otp.trim().length < 4} className={primaryBtn}>
        {pending ? "Checking…" : "Finish signing in"}
      </button>
      {!!error && <SignInFailure error={error} step="code" retry={onSubmit} onNewCode={onNewCode} />}
      <div className="flex flex-wrap gap-4 text-xs">
        <button type="button" onClick={onNewCode} className={link}>
          No code? Start again
        </button>
        <button type="button" onClick={onDifferent} className={link}>
          Use a different account
        </button>
      </div>
    </form>
  );
}
