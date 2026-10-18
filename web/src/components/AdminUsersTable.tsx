import { useEffect, useMemo, useState } from "react";
import type { AdminUserRow } from "../lib/api";
import { connection, filterUsers, lastData, noDataAlert, purge, type UserFilter } from "../lib/adminUsers";
import { useImpersonationDisabled, useRivianActions } from "../lib/config";
import { startImpersonation } from "../lib/impersonation";
import { TypedConfirmDialog } from "./TypedConfirmDialog";

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
  const rivianActions = useRivianActions();
  const narrow = useNarrow();
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

      {narrow ? (
        <ul data-admin-users-cards className="space-y-3">
          {shown.map((u) => (
            <li key={u.id} data-user-row={u.id} onClick={opensUser(onSelect, u.id)} className={`cursor-pointer rounded-lg border border-neutral-800 p-3 hover:bg-neutral-900/50 ${u.id === selectedID ? "bg-emerald-950/20" : ""}`}>
              <UserBlock u={u} selected={u.id === selectedID} onSelect={onSelect} />
              <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
                <div>
                  <dt className="text-xs text-neutral-400">Rivian</dt>
                  <dd><RivianCell u={u} now={now} /></dd>
                </div>
                <div>
                  <dt className="text-xs text-neutral-400">Last data</dt>
                  <dd><LastDataCell u={u} now={now} /></dd>
                </div>
                <div>
                  <dt className="text-xs text-neutral-400">Last seen</dt>
                  <dd className="tabular-nums text-neutral-300">{ago(u.last_seen_at, now)}</dd>
                </div>
                <div>
                  <dt className="text-xs text-neutral-400">Purge</dt>
                  <dd><PurgeCell u={u} now={now} /></dd>
                </div>
              </dl>
              <div className="mt-3 flex flex-wrap gap-1.5">
                <RowActions u={u} busy={busyID === u.id} adminCount={adminCount} actions={actions} impersonationDisabled={impersonationDisabled} rivianActions={rivianActions} onDelete={setDeleting} />
              </div>
            </li>
          ))}
          {shown.length === 0 && <li className="px-3 py-6 text-center text-sm text-neutral-400">No users match.</li>}
        </ul>
      ) : (
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
              {shown.map((u) => (
                <tr key={u.id} data-user-row={u.id} onClick={opensUser(onSelect, u.id)} className={`cursor-pointer border-t border-neutral-800 align-top hover:bg-neutral-900/50 ${u.id === selectedID ? "bg-emerald-950/20" : ""}`}>
                  <td className="px-3 py-2"><UserBlock u={u} selected={u.id === selectedID} onSelect={onSelect} /></td>
                  <td className="px-3 py-2"><RivianCell u={u} now={now} /></td>
                  <td className="px-3 py-2"><LastDataCell u={u} now={now} /></td>
                  <td className="px-3 py-2 tabular-nums text-neutral-300" title={u.last_seen_at ?? ""}>{ago(u.last_seen_at, now)}</td>
                  <td className="px-3 py-2"><PurgeCell u={u} now={now} /></td>
                  <td className="px-3 py-2">
                    <div className="ml-auto flex max-w-[200px] flex-wrap justify-end gap-1.5">
                      <RowActions u={u} busy={busyID === u.id} adminCount={adminCount} actions={actions} impersonationDisabled={impersonationDisabled} rivianActions={rivianActions} onDelete={setDeleting} />
                    </div>
                  </td>
                </tr>
              ))}
              {shown.length === 0 && (
                <tr>
                  <td colSpan={6} className="px-3 py-6 text-center text-sm text-neutral-400">No users match.</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {deleting && (
        <TypedConfirmDialog
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

// A click anywhere on a user's row or card opens them, except on the row's own controls (the actions,
// the name button): those keep their own meaning.
const opensUser = (onSelect: (id: string) => void, id: string) => (e: React.MouseEvent) => {
  if ((e.target as HTMLElement).closest("button, a, input, select, textarea, label, summary")) return;
  onSelect(id);
};

// A phone gets stacked cards instead of a wide table (a table that scrolls sideways clips the actions).
function useNarrow(maxPx = 767): boolean {
  const q = `(max-width: ${maxPx}px)`;
  const [narrow, setNarrow] = useState(() => typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(q).matches);
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const m = window.matchMedia(q);
    const on = () => setNarrow(m.matches);
    on();
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, [q]);
  return narrow;
}

function UserBlock({ u, selected, onSelect }: { u: AdminUserRow; selected: boolean; onSelect: (id: string) => void }) {
  const name = u.display_name || u.username;
  return (
    <>
      <button type="button" aria-pressed={selected} aria-label={`Select ${name}`} onClick={() => onSelect(u.id)} className="text-left font-medium text-neutral-100 hover:underline">
        {name}
      </button>
      <div className="break-all text-xs text-neutral-400">{u.email || u.username}</div>
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
    </>
  );
}

function RivianCell({ u, now }: { u: AdminUserRow; now: number }) {
  const conn = connection(u);
  return (
    <span data-col="rivian" className="block">
      {conn.state === "connected" && <span className="text-emerald-300">Connected</span>}
      {conn.state === "none" && <span className="text-neutral-400">Not connected</span>}
      {conn.state === "reauth" && (
        <span className="text-rose-300">
          Needs re-auth
          <span className="block text-xs text-neutral-400">since {ago(conn.since, now)}</span>
        </span>
      )}
    </span>
  );
}

function LastDataCell({ u, now }: { u: AdminUserRow; now: number }) {
  const data = lastData(u, now);
  const quiet = noDataAlert(u, now);
  return (
    <span data-col="last-data" className="block tabular-nums" title={data.kind === "at" ? data.at : undefined}>
      {data.kind === "unknown" && <span className="text-neutral-500">—</span>}
      {data.kind === "none" && <span className="text-neutral-400">none</span>}
      {data.kind === "at" && <span className={quiet ? "text-rose-300" : "text-neutral-200"}>{ago(data.at, now)}</span>}
      {quiet && (
        <span data-no-data-alert className="mt-0.5 block w-fit rounded-full border border-rose-700 bg-rose-950/50 px-2 py-0.5 text-[11px] font-medium text-rose-200">
          Leased, no data 2h+
        </span>
      )}
    </span>
  );
}

function PurgeCell({ u, now }: { u: AdminUserRow; now: number }) {
  const pg = purge(u, now);
  return (
    <span data-col="purge" className="block">
      {pg.state === "pending" ? (
        <span className={pg.daysLeft <= 30 ? "text-amber-300" : "text-neutral-300"}>
          {pg.daysLeft === 0 ? "Purge due" : `Purge in ${pg.daysLeft}d`}
          {pg.warned && <span className="block text-xs text-neutral-400">warned</span>}
        </span>
      ) : (
        <span className="text-neutral-500">—</span>
      )}
    </span>
  );
}

function RowActions({ u, busy, adminCount, actions, impersonationDisabled, rivianActions, onDelete }: { u: AdminUserRow; busy: boolean; adminCount: number; actions: UserActions; impersonationDisabled: boolean; rivianActions: boolean; onDelete: (u: AdminUserRow) => void }) {
  const name = u.display_name || u.username;
  return (
    <>
      {rivianActions && <RowButton label={`Sync ${name}`} onClick={() => actions.sync(u)} disabled={busy}>Sync</RowButton>}
      {rivianActions && <RowButton label={`Refresh Rivian session for ${name}`} onClick={() => actions.refresh(u)} disabled={busy}>Refresh</RowButton>}
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
      <RowButton label={`Delete ${name}`} tone="danger" onClick={() => onDelete(u)} disabled={busy || (u.role === "admin" && adminCount <= 1)}>Delete…</RowButton>
    </>
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
