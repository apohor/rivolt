import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, backend, type Passkey } from "../lib/api";
import { formatDateTime } from "../lib/format";
import { addFailureMessage, createPasskey, passkeysSupported } from "../lib/passkeys";
import { Card } from "./ui";

// Passkeys: sign in to Rivolt with the phone's or computer's own unlock (Face ID, Touch ID, a fingerprint, a PIN) instead of the
// password. Added from here after asking for the password again; the password keeps working, so losing every passkey locks no one out.
// Not on an install without passkeys (404), nor for an admin viewing as someone or the app (403).
export function PasskeysCard() {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["auth", "passkeys"], queryFn: () => backend.passkeys(), retry: 0, staleTime: 15_000 });
  const [adding, setAdding] = useState(false);
  const [label, setLabel] = useState("");
  const [password, setPassword] = useState("");
  const [note, setNote] = useState<{ text: string; bad: boolean } | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<{ id: string; label: string } | null>(null);
  const refresh = () => qc.invalidateQueries({ queryKey: ["auth", "passkeys"] });

  const add = useMutation({
    mutationFn: async () => {
      const begin = await backend.passkeyRegisterBegin(password);
      const credential = await createPasskey(begin.options);
      if (credential === null) return false; // cancelled in the browser's own prompt
      await backend.passkeyRegisterFinish({ ceremony_id: begin.ceremony_id, label: label.trim() || "Passkey", credential });
      return true;
    },
    onSuccess: (added) => {
      setPassword("");
      if (added) {
        setAdding(false);
        setLabel("");
        setNote({ text: "Passkey added. We emailed you a notice.", bad: false });
        void refresh();
      } else {
        setNote({ text: "Cancelled. No passkey was added.", bad: false });
      }
    },
    onError: (e) => {
      setPassword("");
      setNote({ text: addFailureMessage(e), bad: true });
    },
  });
  const rename = useMutation({
    mutationFn: (v: { id: string; label: string }) => backend.passkeyRename(v.id, v.label.trim() || "Passkey"),
    onSuccess: () => {
      setRenaming(null);
      void refresh();
    },
    onError: () => setNote({ text: "Couldn't rename it. Try again.", bad: true }),
  });
  const remove = useMutation({
    mutationFn: (id: string) => backend.passkeyDelete(id),
    onSuccess: () => {
      setConfirming(null);
      setNote({ text: "Passkey removed.", bad: false });
      void refresh();
    },
    onError: () => setNote({ text: "Couldn't remove it. Try again.", bad: true }),
  });

  if (list.isError && list.error instanceof ApiError && (list.error.status === 404 || list.error.status === 403)) return null;
  if (list.isPending)
    return (
      <Card title="Passkeys" id="passkeys">
        <p className="text-xs text-neutral-500">Loading…</p>
      </Card>
    );
  if (list.isError)
    return (
      <Card title="Passkeys" id="passkeys">
        <p className="text-xs text-rose-400">Couldn't load your passkeys.</p>
      </Card>
    );

  const rows = list.data.passkeys;
  const full = rows.length >= list.data.max;
  const supported = passkeysSupported();
  const busy = add.isPending || rename.isPending || remove.isPending;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setNote(null);
    add.mutate();
  };

  return (
    <Card title="Passkeys" id="passkeys">
      <div data-passkeys className="space-y-3">
        <p className="max-w-xl text-xs text-neutral-500">
          Sign in with your face, fingerprint or device PIN instead of typing the password. Your password still works, and removing a
          passkey needs no password. We email you whenever one is added.
        </p>
        {rows.length === 0 ? (
          <p className="text-sm text-neutral-400">No passkeys yet.</p>
        ) : (
          <ul className="divide-y divide-neutral-800 rounded-lg border border-neutral-800">
            {rows.map((p: Passkey) => (
              <li key={p.id} data-passkey-row className="flex flex-wrap items-center justify-between gap-3 px-3 py-2.5">
                <div className="min-w-0">
                  {renaming?.id === p.id ? (
                    <form
                      className="flex items-center gap-2"
                      onSubmit={(e) => {
                        e.preventDefault();
                        rename.mutate(renaming);
                      }}
                    >
                      <input
                        aria-label="Passkey name"
                        autoFocus
                        maxLength={60}
                        value={renaming.label}
                        onChange={(e) => setRenaming({ id: p.id, label: e.target.value })}
                        className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
                      />
                      <button type="submit" disabled={busy} className="rounded-md bg-emerald-700 px-2.5 py-1 text-xs font-semibold text-white hover:bg-emerald-600 disabled:opacity-50">
                        Save
                      </button>
                      <button type="button" onClick={() => setRenaming(null)} className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-300">
                        Cancel
                      </button>
                    </form>
                  ) : (
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-neutral-100">
                      <span>{p.label}</span>
                      <span className="rounded bg-neutral-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-neutral-400">
                        {p.synced ? "Synced" : "This device"}
                      </span>
                    </div>
                  )}
                  <div className="text-xs text-neutral-500">
                    Added {formatDateTime(p.created_at)} · {p.last_used_at ? `last used ${formatDateTime(p.last_used_at)}` : "not used yet"}
                  </div>
                </div>
                {renaming?.id !== p.id &&
                  (confirming === p.id ? (
                    <span className="flex items-center gap-2 text-xs">
                      <button type="button" disabled={busy} onClick={() => remove.mutate(p.id)} className="rounded-md bg-rose-700 px-2.5 py-1 font-semibold text-white hover:bg-rose-600 disabled:opacity-50">
                        Yes, remove
                      </button>
                      <button type="button" disabled={busy} onClick={() => setConfirming(null)} className="rounded-md border border-neutral-700 px-2.5 py-1 text-neutral-300">
                        Cancel
                      </button>
                    </span>
                  ) : (
                    <span className="flex items-center gap-2 text-xs">
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => {
                          setNote(null);
                          setRenaming({ id: p.id, label: p.label });
                        }}
                        aria-label={`Rename ${p.label}`}
                        className="rounded-md border border-neutral-700 px-2.5 py-1 text-neutral-300 hover:border-neutral-500 disabled:opacity-50"
                      >
                        Rename
                      </button>
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => {
                          setNote(null);
                          setConfirming(p.id);
                        }}
                        aria-label={`Remove ${p.label}`}
                        className="rounded-md border border-neutral-700 px-2.5 py-1 text-neutral-300 hover:border-neutral-500 disabled:opacity-50"
                      >
                        Remove
                      </button>
                    </span>
                  ))}
              </li>
            ))}
          </ul>
        )}
        {note && (
          <p role="status" data-passkeys-note className={`text-xs ${note.bad ? "text-rose-400" : "text-neutral-400"}`}>
            {note.text}
          </p>
        )}
        {!supported ? (
          <p className="text-xs text-neutral-500">This browser can't make a passkey. Use a current Safari, Chrome, Edge or Firefox.</p>
        ) : adding ? (
          <form onSubmit={submit} className="space-y-2 rounded-lg border border-neutral-800 p-3">
            <label className="flex flex-col gap-1 text-xs text-neutral-400">
              Name (so you can tell them apart)
              <input
                value={label}
                maxLength={60}
                placeholder="Passkey"
                onChange={(e) => setLabel(e.target.value)}
                className="w-full max-w-xs rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100"
              />
            </label>
            <label className="flex flex-col gap-1 text-xs text-neutral-400">
              Your password, to confirm it's you
              <input
                type="password"
                required
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full max-w-xs rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100"
              />
            </label>
            <div className="flex items-center gap-2">
              <button type="submit" disabled={busy || password === ""} className="rounded-md bg-emerald-700 px-3 py-1.5 text-xs font-semibold text-white hover:bg-emerald-600 disabled:opacity-50">
                {add.isPending ? "Waiting for your device…" : "Add passkey"}
              </button>
              <button
                type="button"
                disabled={add.isPending}
                onClick={() => {
                  setAdding(false);
                  setPassword("");
                  setNote(null);
                }}
                className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-300"
              >
                Cancel
              </button>
            </div>
          </form>
        ) : (
          <button
            type="button"
            disabled={busy || full}
            onClick={() => {
              setNote(null);
              setAdding(true);
            }}
            className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-200 hover:border-neutral-500 disabled:opacity-50"
          >
            {full ? "You have the most passkeys allowed" : "Add a passkey"}
          </button>
        )}
      </div>
    </Card>
  );
}
