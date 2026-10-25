import { useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { plural, securityFailure, type SecurityFailure } from "../lib/security";
import { Card } from "./ui";

// Settings, Security: change the password, or, if the account may be in someone else's hands, reset everything: a new password, every
// passkey removed and every other device signed out. Both ask for the current password. An account that signs in with Google, GitHub
// or Apple has no Rivolt password, so these hide for it. They show whether or not passkeys are on.
export function AccountSecurityCards() {
  const qc = useQueryClient();
  const [noPassword, setNoPassword] = useState(false);
  if (noPassword) return null;
  const [passkeysLeft, setPasskeysLeft] = useState(false);
  const [cleanup, setCleanup] = useState<string | null>(null);
  const onFailure = (f: SecurityFailure) => {
    if (f.noPassword) setNoPassword(true);
    if (f.passkeysIncomplete) setPasskeysLeft(true);
  };
  // What the reset left undone: every passkey still on the account is removed one by one.
  async function removeAllPasskeys() {
    setCleanup("Removing…");
    try {
      const list = await backend.passkeys();
      for (const p of list.passkeys) await backend.passkeyDelete(p.id);
      setPasskeysLeft(false);
      setCleanup("All passkeys removed.");
      void qc.invalidateQueries({ queryKey: ["auth", "passkeys"] });
    } catch {
      setCleanup("Couldn't remove them all. Try again.");
    }
  }
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["auth", "passkeys"] });
    void qc.invalidateQueries({ queryKey: ["auth", "sessions"] });
  };
  return (
    <>
      <PasswordForm
        id="password"
        title="Change password"
        blurb="Pick a new password. Your other devices are signed out; your passkeys stay."
        button="Change password"
        busyLabel="Changing…"
        onFailure={onFailure}
        run={async (current, next) => {
          const r = await backend.changePassword({ current, new: next });
          refresh();
          const left = r.passkeys?.length ?? 0;
          return `Password changed. ${plural(r.sessions_revoked, "other device was", "other devices were")} signed out.${left > 0 ? ` Your passkeys are listed below: remove any you don't recognise.` : ""}`;
        }}
      />
      {passkeysLeft && (
        <Card title="Finish removing passkeys" id="reset-cleanup">
          <div data-passkeys-cleanup className="space-y-2">
            <p className="text-xs text-neutral-400">Your password is changed. Some passkeys are still on your account.</p>
            <button type="button" onClick={() => void removeAllPasskeys()} className="rounded-md bg-rose-700 px-3 py-1.5 text-xs font-semibold text-white hover:bg-rose-600">
              Remove all passkeys
            </button>
            {cleanup && <p role="status" className="text-xs text-neutral-400">{cleanup}</p>}
          </div>
        </Card>
      )}
      <PasswordForm
        id="reset"
        title="Reset password and remove all passkeys"
        blurb="Use this if someone else may have got into your account. It sets a new password, removes every passkey (yours too, so add them again afterwards) and signs out every other device."
        button="Reset password and remove all passkeys"
        busyLabel="Resetting…"
        danger
        onFailure={onFailure}
        run={async (current, next) => {
          const r = await backend.resetAccountAccess({ current, new: next });
          refresh();
          return `Done. Password changed, ${plural(r.passkeys_removed, "passkey", "passkeys")} removed and ${plural(r.sessions_revoked, "other device", "other devices")} signed out.`;
        }}
      />
    </>
  );
}

function PasswordForm({
  id,
  title,
  blurb,
  button,
  busyLabel,
  danger,
  run,
  onFailure,
}: {
  id: string;
  title: string;
  blurb: string;
  button: string;
  busyLabel: string;
  danger?: boolean;
  run: (current: string, next: string) => Promise<string>;
  onFailure: (f: SecurityFailure) => void;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [note, setNote] = useState<{ text: string; bad: boolean } | null>(null);
  const mismatch = again !== "" && again !== next;
  const ready = current !== "" && next !== "" && again === next && !busy;

  async function go() {
    setBusy(true);
    setNote(null);
    try {
      const text = await run(current, next);
      setNote({ text, bad: false });
      setCurrent("");
      setNext("");
      setAgain("");
    } catch (e) {
      const f = securityFailure(e);
      onFailure(f);
      setNote({ text: f.text, bad: !f.signOutIncomplete });
      setCurrent("");
      if (f.signOutIncomplete) {
        setNext("");
        setAgain("");
      }
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!ready) return;
    if (danger && !confirming) {
      setConfirming(true);
      return;
    }
    void go();
  };

  return (
    <Card title={title} id={id}>
      <form data-security-form={id} onSubmit={submit} className="max-w-sm space-y-3">
        <p className="text-xs text-neutral-500">{blurb}</p>
        <label className="flex flex-col gap-1 text-xs text-neutral-400">
          Current password
          <input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100" />
        </label>
        <label className="flex flex-col gap-1 text-xs text-neutral-400">
          New password
          <input type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100" />
        </label>
        <label className="flex flex-col gap-1 text-xs text-neutral-400">
          New password again
          <input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100" />
        </label>
        {mismatch && <p className="text-xs text-rose-400">The two new passwords differ.</p>}
        {danger && confirming && (
          <p role="alert" className="rounded-md border border-rose-900 bg-rose-950/40 p-2 text-xs text-rose-200">
            This removes every passkey on your account, including your own, and signs out every other device. Press the button again to do it.
          </p>
        )}
        <button
          type="submit"
          disabled={!ready}
          className={`rounded-md px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50 ${danger ? "bg-rose-700 hover:bg-rose-600" : "bg-emerald-700 hover:bg-emerald-600"}`}
        >
          {busy ? busyLabel : danger && confirming ? "Yes, reset everything" : button}
        </button>
        {note && (
          <p role="status" data-security-note className={`text-xs ${note.bad ? "text-rose-400" : "text-neutral-400"}`}>
            {note.text}
          </p>
        )}
      </form>
    </Card>
  );
}
