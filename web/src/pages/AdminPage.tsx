import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminUserRow, type InviteCode } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { AIProvidersPanel, RecapWeatherPanel } from "./SettingsPage";

// AdminPage is gated server-side by requireAdminMW + client-side by
// the AppLayout nav check. We still defensively render an
// "unauthorized" message if a non-admin reaches this route directly
// (e.g. by typing /admin into the URL); the API calls would 403
// anyway but the empty-state is more honest than a list-of-errors.
export default function AdminPage() {
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => backend.whoami(),
    staleTime: 5 * 60_000,
  });
  if (me.isLoading) return <Spinner />;
  if (!me.data || me.data.role !== "admin") {
    return (
      <div className="space-y-4">
        <PageHeader title="Admin" />
        <Card title="Not authorized">
          <p className="text-sm text-neutral-400">
            This page is only available to administrators.
          </p>
        </Card>
      </div>
    );
  }
  return (
    <div className="space-y-4">
      <PageHeader title="Admin" />
      <Card title="Invite codes">
        <InviteCodesPanel />
      </Card>
      <Card title="Users">
        <CreateUserForm />
        <UsersPanel currentUserID={me.data.user_id} />
      </Card>
      <Card title="AI providers">
        <AIProvidersPanel />
      </Card>
      <Card title="Recap weather">
        <RecapWeatherPanel />
      </Card>
    </div>
  );
}

// ── Invite codes ──────────────────────────────────────────────────────────────

function InviteCodesPanel() {
  const qc = useQueryClient();
  const [count, setCount] = useState(1);
  const [newCodes, setNewCodes] = useState<string[] | null>(null);

  const list = useQuery({
    queryKey: ["admin", "invite-codes"],
    queryFn: () => backend.adminListInviteCodes(),
  });

  const generate = useMutation({
    mutationFn: () => backend.adminGenerateInviteCodes(count),
    onSuccess: (data) => {
      setNewCodes(data.codes);
      qc.invalidateQueries({ queryKey: ["admin", "invite-codes"] });
    },
  });

  function copyAll() {
    if (!newCodes) return;
    navigator.clipboard.writeText(newCodes.join("\n"));
  }

  return (
    <div className="space-y-4">
      {/* Generator row */}
      <div className="flex flex-wrap items-end gap-3">
        <div>
          <label className="mb-1 block text-xs text-neutral-500">Count</label>
          <input
            type="number"
            min={1}
            max={100}
            value={count}
            onChange={(e) => setCount(Math.max(1, Math.min(100, parseInt(e.target.value) || 1)))}
            className="w-20 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100 focus:border-emerald-600 focus:outline-none"
          />
        </div>
        <button
          type="button"
          onClick={() => generate.mutate()}
          disabled={generate.isPending}
          className="rounded-md bg-emerald-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-600 disabled:opacity-50"
        >
          {generate.isPending ? "Generating…" : "Generate"}
        </button>
      </div>

      {/* Freshly generated codes — shown until next generate or page reload */}
      {newCodes && newCodes.length > 0 && (
        <div className="rounded-md border border-emerald-900 bg-emerald-950/40 p-3">
          <div className="mb-2 flex items-center justify-between">
            <span className="text-xs font-medium text-emerald-400">
              {newCodes.length} new code{newCodes.length > 1 ? "s" : ""} — copy before leaving
            </span>
            <button
              type="button"
              onClick={copyAll}
              className="text-xs text-emerald-500 hover:underline"
            >
              Copy all
            </button>
          </div>
          <ul className="space-y-1">
            {newCodes.map((c) => (
              <li key={c} className="flex items-center justify-between gap-2">
                <code className="font-mono text-xs text-emerald-300">{c}</code>
                <button
                  type="button"
                  onClick={() => navigator.clipboard.writeText(c)}
                  className="text-[10px] text-neutral-500 hover:text-neutral-300"
                >
                  copy
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {generate.isError && (
        <ErrorBox title={String(generate.error)} />
      )}

      {/* History table */}
      {list.isLoading && <p className="text-sm text-neutral-500">Loading…</p>}
      {list.isError && <ErrorBox title="Failed to load invite codes" />}
      {list.data && <InviteCodeTable codes={list.data.codes} />}
    </div>
  );
}

function InviteCodeTable({ codes }: { codes: InviteCode[] }) {
  if (codes.length === 0) {
    return <p className="text-sm text-neutral-500">No invite codes yet.</p>;
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-xs text-neutral-400">
        <thead>
          <tr className="border-b border-neutral-800">
            <th className="pb-2 font-medium text-neutral-500">Code</th>
            <th className="pb-2 font-medium text-neutral-500">Created</th>
            <th className="pb-2 font-medium text-neutral-500">Used by</th>
            <th className="pb-2 font-medium text-neutral-500">Used at</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-neutral-900">
          {codes.map((c) => (
            <tr key={c.Code}>
              <td className="py-2 pr-4 font-mono text-neutral-200">{c.Code}</td>
              <td className="py-2 pr-4 text-neutral-500">
                {new Date(c.CreatedAt).toLocaleDateString()}
              </td>
              <td className="py-2 pr-4">
                {c.UsedBy ?? <span className="text-neutral-700">—</span>}
              </td>
              <td className="py-2">
                {c.UsedAt ? (
                  new Date(c.UsedAt).toLocaleDateString()
                ) : (
                  <span className="text-emerald-700">available</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function CreateUserForm() {
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [role, setRole] = useState<"user" | "admin">("user");
  const [disabled, setDisabled] = useState(false);
  // One-time password surfaced when the server's Authelia
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
        username: username.trim(),
        email: email.trim() || undefined,
        display_name: displayName.trim(),
        role,
        disabled: disabled || undefined,
      }),
    onSuccess: (resp) => {
      const created = username.trim();
      setUsername("");
      setEmail("");
      setDisplayName("");
      setRole("user");
      setDisabled(false);
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      if (resp.password) {
        setOneTimePassword({ username: created, password: resp.password });
      }
    },
  });

  // Pre-provision: the row reserves the deterministic UUID for the
  // username so when the user later signs in via OIDC with a
  // matching preferred_username the existing row is reused. We
  // surface this in the help text so it's not surprising.
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!username.trim()) return;
        create.mutate();
      }}
      className="mb-4 grid grid-cols-1 gap-2 rounded-md border border-neutral-800 bg-neutral-950 p-3 sm:grid-cols-6"
    >
      <input
        type="text"
        required
        placeholder="username"
        value={username}
        onChange={(e) => setUsername(e.target.value)}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      />
      <input
        type="email"
        placeholder="email (optional)"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      />
      <input
        type="text"
        required
        placeholder="display name"
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      />
      <select
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
        disabled={!username.trim() || create.isPending}
        className="rounded-md border border-emerald-800 bg-emerald-950 px-3 py-1 text-sm text-emerald-200 hover:border-emerald-700 hover:text-emerald-100 disabled:opacity-40"
      >
        {create.isPending ? "Creating…" : "Create user"}
      </button>
      <p className="col-span-full text-xs text-neutral-500">
        Auth is OIDC-only — this pre-provisions the user row. When the
        named user later signs in via OIDC with a matching
        preferred_username, they pick up the pre-set role and details.
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
            <code className="select-all rounded bg-neutral-900 px-2 py-1 font-mono text-sm text-amber-200">
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
              onClick={() => setOneTimePassword(null)}
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

function UsersPanel({ currentUserID }: { currentUserID: string }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["admin", "users"],
    queryFn: () => backend.adminListUsers(),
  });
  const setRole = useMutation({
    mutationFn: ({ id, role }: { id: string; role: "user" | "admin" }) =>
      backend.adminSetUserRole(id, role),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
  const del = useMutation({
    mutationFn: (id: string) => backend.adminDeleteUser(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
  const setDisabled = useMutation({
    mutationFn: ({ id, disabled }: { id: string; disabled: boolean }) =>
      backend.adminSetUserDisabled(id, disabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });

  const rows: AdminUserRow[] = useMemo(() => q.data?.users ?? [], [q.data]);
  const [busyID, setBusyID] = useState<string | null>(null);
  // The "demote / delete the last admin" guard exists server-side
  // (returns 409). The client mirrors it as a button disable so
  // the destructive action doesn't even appear clickable.
  const adminCount = rows.filter((u) => u.role === "admin" && !u.disabled).length;

  if (q.isLoading) return <Spinner />;
  if (q.isError)
    return <ErrorBox title="Failed to load users" detail={String(q.error)} />;

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="text-left text-neutral-500">
          <tr>
            <th className="py-2 pr-3">User</th>
            <th className="py-2 pr-3">Email</th>
            <th className="py-2 pr-3">Role</th>
            <th className="py-2 pr-3">Created</th>
            <th className="py-2 pr-3 text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((u) => {
            const isSelf = u.id === currentUserID;
            const isLastAdmin = u.role === "admin" && adminCount <= 1;
            const busy = busyID === u.id;
            return (
              <tr key={u.id} className="border-t border-neutral-800">
                <td className="py-2 pr-3">
                  <div className="text-neutral-100">
                    {u.display_name || u.username}
                  </div>
                  <div className="text-xs text-neutral-500">{u.username}</div>
                </td>
                <td className="py-2 pr-3 text-neutral-400">{u.email || "—"}</td>
                <td className="py-2 pr-3">
                  <span
                    className={`rounded-full border px-2 py-0.5 text-xs ${
                      u.role === "admin"
                        ? "border-emerald-700 bg-emerald-950 text-emerald-300"
                        : "border-neutral-800 bg-neutral-900 text-neutral-400"
                    }`}
                  >
                    {u.role}
                  </span>
                  {u.disabled && (
                    <span className="ml-2 rounded-full border border-amber-800 bg-amber-950 px-2 py-0.5 text-xs text-amber-300">
                      disabled
                    </span>
                  )}
                </td>
                <td className="py-2 pr-3 text-neutral-500">
                  {new Date(u.created_at).toLocaleDateString()}
                </td>
                <td className="py-2 pr-3">
                  <div className="flex items-center justify-end gap-2">
                    {u.role === "user" ? (
                      <button
                        type="button"
                        disabled={busy}
                        onClick={async () => {
                          setBusyID(u.id);
                          try {
                            await setRole.mutateAsync({ id: u.id, role: "admin" });
                          } finally {
                            setBusyID(null);
                          }
                        }}
                        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-xs text-neutral-300 hover:border-neutral-700 hover:text-neutral-100 disabled:opacity-40"
                      >
                        Promote
                      </button>
                    ) : (
                      <button
                        type="button"
                        disabled={busy || isLastAdmin}
                        title={isLastAdmin ? "Cannot demote the last admin" : ""}
                        onClick={async () => {
                          setBusyID(u.id);
                          try {
                            await setRole.mutateAsync({ id: u.id, role: "user" });
                          } finally {
                            setBusyID(null);
                          }
                        }}
                        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-xs text-neutral-300 hover:border-neutral-700 hover:text-neutral-100 disabled:opacity-40"
                      >
                        Demote
                      </button>
                    )}
                    <button
                      type="button"
                      disabled={
                        busy ||
                        (!u.disabled && isSelf) ||
                        (!u.disabled && u.role === "admin" && isLastAdmin)
                      }
                      title={
                        !u.disabled && isSelf
                          ? "Cannot disable your own account"
                          : !u.disabled && u.role === "admin" && isLastAdmin
                          ? "Cannot disable the last admin"
                          : u.disabled
                          ? "Re-enable sign-in for this user"
                          : "Block this user from minting new sessions"
                      }
                      onClick={async () => {
                        setBusyID(u.id);
                        try {
                          await setDisabled.mutateAsync({
                            id: u.id,
                            disabled: !u.disabled,
                          });
                        } finally {
                          setBusyID(null);
                        }
                      }}
                      className={`rounded-md border px-2 py-1 text-xs disabled:opacity-40 ${
                        u.disabled
                          ? "border-emerald-800 bg-emerald-950/40 text-emerald-300 hover:border-emerald-700 hover:text-emerald-200"
                          : "border-amber-900 bg-amber-950/40 text-amber-300 hover:border-amber-800 hover:text-amber-200"
                      }`}
                    >
                      {u.disabled ? "Enable" : "Disable"}
                    </button>
                    <button
                      type="button"
                      disabled={busy || isSelf || isLastAdmin}
                      title={
                        isSelf
                          ? "Cannot delete your own account"
                          : isLastAdmin
                          ? "Cannot delete the last admin"
                          : ""
                      }
                      onClick={async () => {
                        if (
                          !confirm(
                            `Permanently delete ${u.username}? Their drives, charges, and settings will be removed.`,
                          )
                        )
                          return;
                        setBusyID(u.id);
                        try {
                          await del.mutateAsync(u.id);
                        } finally {
                          setBusyID(null);
                        }
                      }}
                      className="rounded-md border border-red-900 bg-red-950/40 px-2 py-1 text-xs text-red-300 hover:border-red-800 hover:text-red-200 disabled:opacity-40"
                    >
                      Delete
                    </button>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {(setRole.error || del.error) && (
        <p className="mt-2 text-xs text-red-400">
          {String(setRole.error ?? del.error)}
        </p>
      )}
    </div>
  );
}
