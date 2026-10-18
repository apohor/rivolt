import { useMemo, useState } from "react";
import type { AdminUserRow } from "../lib/api";
import { connection, filterUsers, lastData, noDataAlert, purge, type UserFilter } from "../lib/adminUsers";
import { useImpersonationDisabled } from "../lib/config";
import { startImpersonation } from "../lib/impersonation";

// The admin Users table (new look): one dense row per user with the things an operator scans for:
// is Rivian connected, when did data last arrive (red when a leased truck has gone quiet), when was
// the user last seen, and the inactive-account purge countdown. Row actions sit in the row; delete
// asks for the account's name to be typed first.
export type UserActions = {
  sync: (u: AdminUserRow) => void;
  refresh: (u: AdminUserRow) => void;
  toggleDisabled: (u: AdminUserRow) => void;
  remove: (u: AdminUserRow) => void;
};

const CHIPS: { id: UserFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "attention", label: "Needs attention" },
  { id: "reauth", label: "Re-auth" },
  { id: "nodata", label: "No data" },
  { id: "admins", label: "Admins" },
  { id: "disabled", label: "Disabled" },
];

function ago(iso: string | null | undefined, now: number): string {
  if (!iso) return "never";
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "—";
  const s = Math.max(0, Math.floor((now - t) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function AdminUsersTable({
  rows, selectedID, onSelect, busyID, adminCount, actions, now = Date.now(),
}: {
  rows: AdminUserRow[];
  selectedID: string | null;
  onSelect: (id: string) => void;
  busyID: string | null;
  adminCount: number;
  actions: UserActions;
  now?: number;
}) {
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<UserFilter>("all");
  const [deleting, setDeleting] = useState<AdminUserRow | null>(null);
  const impersonationDisabled = useImpersonationDisabled();
  const shown = useMemo(() => filterUsers(rows, q, filter, now), [rows, q, filter, now]);
  const counts = useMemo(
    () => Object.fromEntries(CHIPS.map((c) => [c.id, filterUsers(rows, "", c.id, now).length])) as Record<UserFilter, number>,
    [rows, now],
  );

  return (
    <div data-admin-users className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <input
          type="search"
          aria-label="Search users"
          placeholder="Search name or email"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          className="input !w-full sm:!w-64"
        />
        <div role="group" aria-label="Filter users" className="flex flex-wrap gap-1.5">
          {CHIPS.map((c) => (
            <button
              key={c.id}
              type="button"
              aria-pressed={filter === c.id}
              onClick={() => setFilter(c.id)}
              className={`rounded-full border px-2.5 py-1 text-xs ${
                filter === c.id ? "border-emerald-600 bg-emerald-950/50 text-emerald-200" : "border-neutral-700 text-neutral-300 hover:border-neutral-500"
              }`}
            >
              {c.label} <span className="tabular-nums text-neutral-400">{counts[c.id]}</span>
            </button>
          ))}
        </div>
      </div>

      <div className="overflow-x-auto rounded-md border border-neutral-800">
        <table className="w-full min-w-[780px] text-sm">
          <thead className="text-left text-xs text-neutral-400">
            <tr>
              <th scope="col" className="px-3 py-2">User</th>
              <th scope="col" className="px-3 py-2">Rivian</th>
              <th scope="col" className="px-3 py-2">Last data</th>
              <th scope="col" className="px-3 py-2">Last seen</th>
              <th scope="col" className="px-3 py-2">Purge</th>
              <th scope="col" className="px-3 py-2 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((u) => {
              const conn = connection(u);
              const data = lastData(u, now);
              const quiet = noDataAlert(u, now);
              const pg = purge(u, now);
              const busy = busyID === u.id;
              const name = u.display_name || u.username;
              return (
                <tr key={u.id} data-user-row={u.id} className={`border-t border-neutral-800 align-top ${u.id === selectedID ? "bg-emerald-950/20" : ""}`}>
                  <td className="px-3 py-2">
                    <button
                      type="button"
                      aria-pressed={u.id === selectedID}
                      aria-label={`Select ${name}`}
                      onClick={() => onSelect(u.id)}
                      className="text-left font-medium text-neutral-100 hover:underline"
                    >
                      {name}
                    </button>
                    <div className="text-xs text-neutral-400">{u.email || u.username}</div>
                    <div className="mt-0.5 text-xs text-neutral-400">
                      <span data-col="vehicles">{u.vehicle_count} {u.vehicle_count === 1 ? "vehicle" : "vehicles"}</span>
                      {" · "}
                      <span data-col="receipts">Receipts {u.receipts_opt_in === undefined ? "—" : u.receipts_opt_in ? "on" : "off"}</span>
                      {" · "}
                      <span data-col="joined">Joined {new Date(u.created_at).toLocaleDateString()}</span>
                    </div>
                    <div className="mt-0.5 flex gap-1">
                      {u.role === "admin" && <span className="rounded-full border border-emerald-700 px-1.5 text-[10px] text-emerald-300">admin</span>}
                      {u.disabled && <span className="rounded-full border border-amber-700 px-1.5 text-[10px] text-amber-300">disabled</span>}
                    </div>
                  </td>
                  <td className="px-3 py-2" data-col="rivian">
                    {conn.state === "connected" && <span className="text-emerald-300">Connected</span>}
                    {conn.state === "none" && <span className="text-neutral-400">Not connected</span>}
                    {conn.state === "reauth" && (
                      <span className="text-rose-300">
                        Needs re-auth
                        <span className="block text-xs text-neutral-400">since {ago(conn.since, now)}</span>
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2 tabular-nums" data-col="last-data" title={data.kind === "at" ? data.at : undefined}>
                    {data.kind === "unknown" && <span className="text-neutral-500">—</span>}
                    {data.kind === "none" && <span className="text-neutral-400">none</span>}
                    {data.kind === "at" && <span className={quiet ? "text-rose-300" : "text-neutral-200"}>{ago(data.at, now)}</span>}
                    {quiet && (
                      <span data-no-data-alert className="mt-0.5 block w-fit rounded-full border border-rose-700 bg-rose-950/50 px-2 py-0.5 text-[11px] font-medium text-rose-200">
                        Leased, no data 2h+
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2 tabular-nums text-neutral-300" title={u.last_seen_at ?? ""}>{ago(u.last_seen_at, now)}</td>
                  <td className="px-3 py-2" data-col="purge">
                    {pg.state === "pending" ? (
                      <span className={pg.daysLeft <= 30 ? "text-amber-300" : "text-neutral-300"}>
                        {pg.daysLeft === 0 ? "Purge due" : `Purge in ${pg.daysLeft}d`}
                        {pg.warned && <span className="block text-xs text-neutral-400">warned</span>}
                      </span>
                    ) : (
                      <span className="text-neutral-500">—</span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex max-w-[200px] flex-wrap justify-end gap-1.5 ml-auto">
                      <RowButton label={`Sync ${name}`} onClick={() => actions.sync(u)} disabled={busy}>Sync</RowButton>
                      <RowButton label={`Refresh Rivian session for ${name}`} onClick={() => actions.refresh(u)} disabled={busy}>Refresh</RowButton>
                      <RowButton label={`${u.disabled ? "Enable" : "Disable"} ${name}`} onClick={() => actions.toggleDisabled(u)} disabled={busy || (u.role === "admin" && !u.disabled && adminCount <= 1)}>
                        {u.disabled ? "Enable" : "Disable"}
                      </RowButton>
                      {u.role !== "admin" && !impersonationDisabled && (
                        <RowButton
                          label={`View as ${name}`}
                          onClick={() => {
                            startImpersonation({ id: u.id, email: u.email || u.username });
                            window.location.assign("/");
                          }}
                        >
                          View as
                        </RowButton>
                      )}
                      <RowButton label={`Delete ${name}`} tone="danger" onClick={() => setDeleting(u)} disabled={busy || (u.role === "admin" && adminCount <= 1)}>Delete…</RowButton>
                    </div>
                  </td>
                </tr>
              );
            })}
            {shown.length === 0 && (
              <tr>
                <td colSpan={6} className="px-3 py-6 text-center text-sm text-neutral-400">No users match.</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {deleting && (
        <TypedConfirm
          title={`Delete ${deleting.display_name || deleting.username}?`}
          detail="Their drives, charges and settings are removed permanently."
          phrase={deleting.email || deleting.username}
          confirmLabel="Delete user"
          onCancel={() => setDeleting(null)}
          onConfirm={() => {
            const u = deleting;
            setDeleting(null);
            actions.remove(u);
          }}
        />
      )}
    </div>
  );
}

function RowButton({ children, label, onClick, disabled, tone }: { children: React.ReactNode; label: string; onClick: () => void; disabled?: boolean; tone?: "danger" }) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      disabled={disabled}
      className={`rounded-md border px-2 py-1 text-xs disabled:opacity-40 ${
        tone === "danger" ? "border-rose-800 text-rose-300 hover:border-rose-600" : "border-neutral-700 text-neutral-200 hover:border-neutral-500"
      }`}
    >
      {children}
    </button>
  );
}

// A destructive action that needs the exact name typed before the button works.
export function TypedConfirm({ title, detail, phrase, confirmLabel, onConfirm, onCancel }: { title: string; detail: string; phrase: string; confirmLabel: string; onConfirm: () => void; onCancel: () => void }) {
  const [typed, setTyped] = useState("");
  const ok = typed.trim() === phrase;
  return (
    <div role="alertdialog" aria-label={title} data-typed-confirm className="rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
      <p className="font-medium text-rose-200">{title}</p>
      <p className="mt-1 text-xs text-neutral-300">{detail}</p>
      <label className="mt-2 block text-xs text-neutral-300">
        Type <code className="rounded bg-neutral-900 px-1 text-neutral-100">{phrase}</code> to confirm
        <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" className="input mt-1" />
      </label>
      <div className="mt-3 flex gap-2">
        <button type="button" disabled={!ok} onClick={onConfirm} className="rounded-md bg-rose-700 px-3 py-1.5 text-sm font-semibold text-white hover:bg-rose-600 disabled:opacity-40">
          {confirmLabel}
        </button>
        <button type="button" onClick={onCancel} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500">
          Cancel
        </button>
      </div>
    </div>
  );
}
