import { useEffect, useState } from "react";
import { ApiError, backend, type AdminUserRow } from "../../lib/api";
import { TypedConfirmDialog } from "../TypedConfirmDialog";

// "Reset this account's access": for an account someone else may hold, when the owner cannot do it themselves in Settings → Security.
// One audited call that sets a new random password, ends every session and remembered sign-in, removes every passkey and emails the
// owner. The new password is shown ONCE, here, and held in this component's state only: never stored, logged or put in a URL; it is
// cleared when the panel is closed or the page goes. A step that fails shows which one and no password; running it again is safe.
// (docs/COMPROMISED_ACCOUNT.md has the rules for handing the password over.)

const STEPS: Record<string, string> = {
  set_password: "setting the new password",
  forget_remembered_logins: "forgetting the remembered sign-ins",
  revoke_sessions: "signing the sessions out",
  list_passkeys: "listing the passkeys",
  delete_passkeys: "removing the passkeys",
};

export function stepWords(step: unknown): string {
  return typeof step === "string" && STEPS[step] ? STEPS[step] : "one of its steps";
}

type Outcome =
  | { kind: "done"; password: string; sessions: number; passkeys: number }
  | { kind: "stopped"; step: string }
  | { kind: "none" }
  | { kind: "error" };

export function ResetAccess({ row, disabled }: { row: AdminUserRow; disabled?: boolean }) {
  const [asking, setAsking] = useState(false);
  const [running, setRunning] = useState(false);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [copied, setCopied] = useState(false);
  const mail = row.email || row.username;

  // The password lives only here: gone when another user's page opens (and with the component when the page goes).
  useEffect(() => {
    setOutcome(null);
    setCopied(false);
  }, [row.id]);

  async function run() {
    setAsking(false);
    setRunning(true);
    setOutcome(null);
    setCopied(false);
    try {
      const r = await backend.adminSecureUser(row.id);
      setOutcome({ kind: "done", password: r.temporary_password, sessions: r.sessions_revoked, passkeys: r.passkeys_removed });
    } catch (e) {
      const body = e instanceof ApiError ? ((e.body ?? {}) as { error?: string; step?: string }) : {};
      if (e instanceof ApiError && e.status === 409 && body.error === "no_password_account") setOutcome({ kind: "none" });
      else if (e instanceof ApiError && e.status === 502 && body.error === "incomplete") setOutcome({ kind: "stopped", step: String(body.step ?? "") });
      else setOutcome({ kind: "error" });
    } finally {
      setRunning(false);
    }
  }

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  return (
    <>
      <button
        type="button"
        data-reset-access
        disabled={disabled || running}
        onClick={() => {
          setOutcome(null);
          setAsking(true);
        }}
        className="rounded-md border border-rose-800 px-3 py-1.5 text-sm text-rose-300 hover:border-rose-600 disabled:opacity-40"
      >
        {running ? "Resetting…" : "Reset this account's access…"}
      </button>
      {asking && (
        <div className="basis-full">
          <TypedConfirmDialog
            title={`Reset access for ${mail}?`}
            detail="Use this only when someone else may have the account and its owner cannot do it themselves in Settings, Security. It sets a new random password, signs out every browser and the iPhone app, forgets remembered sign-ins, removes every passkey and emails the owner. The new password is shown once, on this screen."
            phrase={mail}
            confirmLabel="Reset access"
            onCancel={() => setAsking(false)}
            onConfirm={() => void run()}
          />
        </div>
      )}
      {outcome?.kind === "done" && (
        <div role="alertdialog" aria-label="Temporary password" data-temp-password className="basis-full rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
          <p className="font-medium text-amber-200">Access reset. Here is the new password, once.</p>
          <p className="mt-1 text-xs text-neutral-300">
            Sent {outcome.sessions} {outcome.sessions === 1 ? "session" : "sessions"} out and removed {outcome.passkeys} {outcome.passkeys === 1 ? "passkey" : "passkeys"}. The owner has been emailed that support reset the account.
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <code data-temp-password-value className="select-all break-all rounded bg-neutral-900 px-2 py-1 text-neutral-100">{outcome.password}</code>
            <button type="button" onClick={() => void copy(outcome.password)} className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-200 hover:border-neutral-500">
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
          <ul className="mt-2 list-disc space-y-1 pl-5 text-xs text-amber-100">
            <li>Send it ONLY to the email address on the account: <strong>{mail}</strong>. Never to any other address, phone number or chat, even if the person asking says they lost the mailbox.</li>
            <li>Tell them to change it at once in Settings, Security (Change password, using this one as the current password), and to remove any passkey they do not recognise.</li>
            <li>It is not stored or logged anywhere. Closing this clears it for good; if it is lost, run the reset again.</li>
          </ul>
          <button
            type="button"
            onClick={() => {
              setOutcome(null);
              setCopied(false);
            }}
            className="mt-3 rounded-md bg-neutral-700 px-3 py-1.5 text-sm font-semibold text-white hover:bg-neutral-600"
          >
            Done, clear it
          </button>
        </div>
      )}
      {outcome?.kind === "stopped" && (
        <div role="alert" data-reset-stopped className="basis-full rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
          <p className="font-medium text-rose-200">It stopped while {stepWords(outcome.step)}. No password was returned.</p>
          <p className="mt-1 text-xs text-neutral-300">The account now holds a random password nobody knows, and the earlier steps are done. Every step can be repeated: run it again to finish.</p>
          <button type="button" onClick={() => void run()} disabled={running} className="mt-2 rounded-md bg-rose-700 px-3 py-1.5 text-sm font-semibold text-white hover:bg-rose-600 disabled:opacity-50">
            Run again
          </button>
        </div>
      )}
      {outcome?.kind === "none" && (
        <p role="status" data-reset-none className="basis-full text-xs text-neutral-300">This account signs in with a provider (Google, GitHub or Apple) and has no Rivolt password to reset.</p>
      )}
      {outcome?.kind === "error" && (
        <p role="alert" data-reset-error className="basis-full text-xs text-rose-300">That didn't work, and no password was returned. Run it again.</p>
      )}
    </>
  );
}
