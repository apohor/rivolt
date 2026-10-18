import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminUserRow } from "../../lib/api";

// The things an admin can do to a user, shared by the list's row actions and the user page. Each one
// runs through `run`, which marks the user busy, refreshes the list and the user, and leaves a one-line
// note (a result or the failure) for the screen to show.
export function useUserActions() {
  const qc = useQueryClient();
  const [busyID, setBusyID] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const refresh = (id: string) => {
    void qc.invalidateQueries({ queryKey: ["admin", "users"] });
    void qc.invalidateQueries({ queryKey: ["admin", "user", id] });
    void qc.invalidateQueries({ queryKey: ["admin", "audit"] });
  };
  const setRole = useMutation({ mutationFn: ({ id, role }: { id: string; role: "user" | "admin" }) => backend.adminSetUserRole(id, role) });
  const setDisabled = useMutation({ mutationFn: ({ id, disabled }: { id: string; disabled: boolean }) => backend.adminSetUserDisabled(id, disabled) });
  const sync = useMutation({ mutationFn: (id: string) => backend.adminSyncRivian(id) });
  const refreshSession = useMutation({ mutationFn: (id: string) => backend.adminRefreshRivianSession(id) });
  const del = useMutation({ mutationFn: (id: string) => backend.adminDeleteUser(id) });

  async function run(u: AdminUserRow, fn: () => Promise<string | void>, failLabel: string): Promise<boolean> {
    setBusyID(u.id);
    setNote(null);
    try {
      const msg = await fn();
      if (msg) setNote(msg);
      refresh(u.id);
      return true;
    } catch (e) {
      setNote(`${failLabel}: ${String((e as Error).message ?? e)}`);
      return false;
    } finally {
      setBusyID(null);
    }
  }

  return {
    busyID,
    note,
    setNote,
    sync: (u: AdminUserRow) =>
      run(u, async () => {
        const r = await sync.mutateAsync(u.id);
        return `Synced: ${r.vehicle_count} vehicle${r.vehicle_count === 1 ? "" : "s"} on file for ${u.username}.`;
      }, "Sync failed"),
    refreshSession: (u: AdminUserRow) => run(u, async () => (await refreshSession.mutateAsync(u.id)).message, "Refresh failed"),
    toggleDisabled: (u: AdminUserRow) =>
      run(u, async () => {
        await setDisabled.mutateAsync({ id: u.id, disabled: !u.disabled });
        return `${u.username} ${u.disabled ? "enabled" : "disabled"}.`;
      }, "Update failed"),
    setRole: (u: AdminUserRow, role: "user" | "admin") =>
      run(u, async () => {
        await setRole.mutateAsync({ id: u.id, role });
        return `${u.username} is now ${role === "admin" ? "an admin" : "a user"}.`;
      }, "Update failed"),
    remove: async (u: AdminUserRow) => {
      const ok = await run(u, async () => {
        const r = await del.mutateAsync(u.id);
        void qc.invalidateQueries({ queryKey: ["admin", "idp-failures"] });
        return r.idp === "failed" ? `${u.username} deleted, but sign-in removal failed (see Sign-in removal failures${r.idp_error ? `: ${r.idp_error}` : ""}).` : `${u.username} deleted.`;
      }, "Delete failed");
      return ok;
    },
  };
}
