import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { backend } from "../../lib/api";

function CreateUserForm({ onDone }: { onDone: (keepOpen: boolean) => void }) {
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [role, setRole] = useState<"user" | "admin">("user");
  const [disabled, setDisabled] = useState(false);
  // One-time password surfaced when the server's IdP (Kratos)
  // integration is wired in. Cleared by the next form submit
  // and on dismiss — never persisted, never refetched. Admin
  // must copy it out before the dialog closes.
  const [oneTimePassword, setOneTimePassword] = useState<{
    username: string;
    password: string;
  } | null>(null);

  const create = useMutation({
    mutationFn: () =>
      backend.adminCreateUser({
        email: email.trim(),
        display_name: displayName.trim() || undefined,
        role,
        disabled: disabled || undefined,
      }),
    onSuccess: (resp) => {
      const created = email.trim();
      setEmail("");
      setDisplayName("");
      setRole("user");
      setDisabled(false);
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      if (resp.password) {
        setOneTimePassword({ username: created, password: resp.password });
        onDone(true); // the one-time password is shown once: keep the form open until it is dismissed
      } else {
        onDone(false);
      }
    },
  });

  // Email is the canonical Kratos credential identifier. Display
  // name is optional — the server defaults it to the email when
  // empty. Role and disabled stay explicit because their defaults
  // matter operationally (a stale "admin" toggle would be a bug).
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!email.trim()) return;
        create.mutate();
      }}
      className="mb-4 grid grid-cols-1 gap-2 rounded-md border border-neutral-800 bg-neutral-950 p-3 sm:grid-cols-5"
    >
      <input
        type="email"
        required
        placeholder="email"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      />
      <input
        type="text"
        placeholder="display name (optional)"
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      />
      <select
        aria-label="Role"
        value={role}
        onChange={(e) => setRole(e.target.value as "user" | "admin")}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      >
        <option value="user">user</option>
        <option value="admin">admin</option>
      </select>
      <label className="flex items-center gap-2 rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-300">
        <input
          type="checkbox"
          checked={disabled}
          onChange={(e) => setDisabled(e.target.checked)}
          className="h-3.5 w-3.5"
        />
        disabled
      </label>
      <button
        type="submit"
        disabled={!email.trim() || create.isPending}
        className="rounded-md border border-emerald-800 bg-emerald-950 px-3 py-1 text-sm text-emerald-200 hover:border-emerald-700 hover:text-emerald-100 disabled:opacity-40"
      >
        {create.isPending ? "Creating…" : "Create user"}
      </button>
      <p className="col-span-full text-xs text-neutral-500">
        Provisions the user in Kratos with a one-time password. They
        sign in at <span className="font-mono">auth.rivolt.dev</span>{" "}
        with this email and the password shown below, then change it
        via the IdP self-service flow.
      </p>
      {create.error && (
        <p className="col-span-full text-xs text-red-400">
          {String(create.error)}
        </p>
      )}
      {oneTimePassword && (
        <div className="col-span-full rounded-md border border-amber-800 bg-amber-950/40 p-3 text-xs text-amber-100">
          <p className="mb-2 font-medium">
            One-time password for{" "}
            <span className="font-mono">{oneTimePassword.username}</span>
          </p>
          <div className="flex items-center gap-2">
            <code className="select-all rounded-sm bg-neutral-900 px-2 py-1 font-mono text-sm text-amber-200">
              {oneTimePassword.password}
            </code>
            <button
              type="button"
              onClick={() => {
                navigator.clipboard?.writeText(oneTimePassword.password);
              }}
              className="rounded-md border border-amber-800 px-2 py-1 text-amber-200 hover:border-amber-700"
            >
              Copy
            </button>
            <button
              type="button"
              onClick={() => {
                setOneTimePassword(null);
                onDone(false);
              }}
              className="ml-auto rounded-md border border-neutral-700 px-2 py-1 text-neutral-300 hover:border-neutral-500"
            >
              Dismiss
            </button>
          </div>
          <p className="mt-2 text-amber-200/80">
            Send this to the user out-of-band. It is shown once and is
            not stored. The user can change it after first sign-in.
          </p>
        </div>
      )}
    </form>
  );
}

// Adding a user is occasional: the list comes first and the form opens on demand. After a create that
// returns a one-time password the form stays open until that password is dismissed.
export default function AddUser() {
  const [open, setOpen] = useState(false);
  return (
    <div data-add-user className="mb-4">
      <button
        type="button"
        aria-expanded={open}
        aria-controls="add-user-form"
        onClick={() => setOpen((v) => !v)}
        className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500"
      >
        {open ? "Close" : "Add user"}
      </button>
      {open && (
        <div id="add-user-form" className="mt-3">
          <CreateUserForm onDone={(keepOpen) => setOpen(keepOpen)} />
        </div>
      )}
    </div>
  );
}
