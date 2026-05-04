import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminUserRow } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { AIProvidersPanel } from "./SettingsPage";

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
      <Card title="Users">
        <UsersPanel currentUserID={me.data.user_id} />
      </Card>
      <Card title="AI providers">
        <AIProvidersPanel />
      </Card>
    </div>
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

  const rows: AdminUserRow[] = useMemo(() => q.data?.users ?? [], [q.data]);
  const [busyID, setBusyID] = useState<string | null>(null);
  // The "demote / delete the last admin" guard exists server-side
  // (returns 409). The client mirrors it as a button disable so
  // the destructive action doesn't even appear clickable.
  const adminCount = rows.filter((u) => u.role === "admin").length;

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
