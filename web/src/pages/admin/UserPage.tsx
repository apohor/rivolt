import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { backend, type AdminUserDetail, type AdminUserRow } from "../../lib/api";
import { agoFrom } from "../../lib/adminFormat";
import { connection, lastData, noDataAlert, purge } from "../../lib/adminUsers";
import { grafanaUserExploreURL } from "../../lib/adminGrafana";
import { useImpersonationDisabled, useRivianActions } from "../../lib/config";
import { startImpersonation } from "../../lib/impersonation";
import { Card, ErrorBox, Spinner } from "../../components/ui";
import { TypedConfirmDialog } from "../../components/TypedConfirmDialog";
import { UserAuditHistory } from "../../components/AdminAuditNext";
import { useUserActions } from "../../components/admin/useUserActions";

// /admin/users/:id: one user, as a page. Identity, the Rivian connection and last data, vehicles,
// activity, purge status, the admin history about them, and the actions. The list is the page before
// this one, so the browser's back button returns to it.
export default function UserPage({ currentUserID }: { currentUserID: string }) {
  const { id = "" } = useParams();
  const list = useQuery({ queryKey: ["admin", "users"], queryFn: () => backend.adminListUsers() });
  const detail = useQuery({ queryKey: ["admin", "user", id], queryFn: () => backend.adminUserDetail(id), enabled: !!id, retry: 0 });
  const row = list.data?.users.find((u) => u.id === id);

  return (
    <div data-user-page={id} className="space-y-4">
      <Link to="/admin/users" className="inline-block text-sm text-emerald-300 underline-offset-2 hover:underline">
        ← All users
      </Link>
      {list.isLoading && <Spinner />}
      {list.isError && <ErrorBox title="Failed to load the user" detail={String((list.error as Error).message)} />}
      {list.data && !row && (
        <Card title="User not found">
          <p className="text-sm text-neutral-400">There is no user with this id. They may have been deleted.</p>
        </Card>
      )}
      {row && <UserBody row={row} d={detail.data} detailError={detail.isError ? String((detail.error as Error).message) : null} users={list.data!.users} currentUserID={currentUserID} />}
    </div>
  );
}

function UserBody({ row, d, detailError, users, currentUserID }: { row: AdminUserRow; d: AdminUserDetail | undefined; detailError: string | null; users: AdminUserRow[]; currentUserID: string }) {
  const now = Date.now();
  const name = row.display_name || row.username;
  const conn = connection(row);
  const data = lastData(row, now);
  const quiet = noDataAlert(row, now);
  const pg = purge(row, now);
  return (
    <>
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="break-words text-2xl font-semibold tracking-tight text-neutral-50">{name}</h1>
          <p className="break-all text-sm text-neutral-400">{row.email || row.username}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className={`rounded-full border px-2 py-0.5 ${row.role === "admin" ? "border-emerald-700 bg-emerald-950 text-emerald-300" : "border-neutral-700 text-neutral-300"}`}>{row.role}</span>
          {row.disabled && <span className="rounded-full border border-amber-700 px-2 py-0.5 text-amber-300">disabled</span>}
          {conn.state === "reauth" && <span className="rounded-full border border-rose-700 px-2 py-0.5 text-rose-300">needs re-auth</span>}
        </div>
      </header>
      {detailError && <ErrorBox title="Couldn't load the details" detail={detailError} />}

      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="Identity">
          <Facts>
            <Fact label="User ID"><code className="break-all text-xs">{row.id}</code></Fact>
            <Fact label="Joined">{new Date(row.created_at).toLocaleString()}</Fact>
            <Fact label="Last seen">{row.last_seen_at ? `${agoFrom(row.last_seen_at)} (${new Date(row.last_seen_at).toLocaleString()})` : "never"}</Fact>
            <Fact label="Active sessions">{d ? d.active_sessions : "…"}</Fact>
            <Fact label="Onboarding">{d ? (d.onboarding_completed ? "Completed" : "Pending") : "…"}</Fact>
            <Fact label="Monthly receipts">{row.receipts_opt_in === undefined ? "—" : row.receipts_opt_in ? "On" : "Off"}</Fact>
          </Facts>
        </Card>

        <Card title="Rivian">
          <Facts>
            <Fact label="Connection">
              {conn.state === "connected" && <span className="text-emerald-300">Connected{d?.rivian_session_at ? ` · session ${agoFrom(d.rivian_session_at)}` : ""}</span>}
              {conn.state === "none" && <span className="text-neutral-400">Not connected</span>}
              {conn.state === "reauth" && (
                <span className="text-rose-300">
                  Needs re-auth{conn.since ? `, since ${agoFrom(conn.since)}` : ""}
                  {d?.needs_reauth_reason && <span className="block text-xs text-neutral-400">{d.needs_reauth_reason}</span>}
                </span>
              )}
            </Fact>
            <Fact label="Last data">
              {data.kind === "unknown" && "—"}
              {data.kind === "none" && <span className="text-neutral-400">none</span>}
              {data.kind === "at" && <span className={quiet ? "text-rose-300" : ""} title={data.at}>{agoFrom(data.at)}</span>}
              {quiet && <span data-no-data-alert className="ml-2 rounded-full border border-rose-700 bg-rose-950/50 px-2 py-0.5 text-[11px] text-rose-200">Leased, no data 2h+</span>}
            </Fact>
            <Fact label="Recording">{row.leased === undefined ? "—" : row.leased ? "Leased to a pod" : "Not leased"}</Fact>
            <Fact label="Vehicles">{row.vehicle_count}</Fact>
          </Facts>
        </Card>

        <Card title="Activity">
          <Facts>
            <Fact label="Drives">{d ? `${d.drive_count} · ${d.drive_miles_total.toFixed(0)} mi` : "…"}</Fact>
            <Fact label="Charges">{d ? `${d.charge_count} · ${d.charge_kwh_total.toFixed(0)} kWh` : "…"}</Fact>
            <Fact label="Samples">{d ? d.sample_count.toLocaleString() : "…"}</Fact>
            <Fact label="Telemetry span"><TelemetrySpan id={row.id} d={d} /></Fact>
            <Fact label="Imported rows">{d ? d.import_count : row.import_count}</Fact>
          </Facts>
        </Card>

        <Card title="Inactive-account purge">
          <div data-purge className="text-sm">
            {pg.state === "pending" ? (
              <p className={pg.daysLeft <= 30 ? "text-amber-300" : "text-neutral-200"}>
                {pg.daysLeft === 0 ? "Due for purge." : `Purged in ${pg.daysLeft} day${pg.daysLeft === 1 ? "" : "s"}.`}
                {pg.warned ? " A warning email was sent." : ""}
              </p>
            ) : (
              <p className="text-neutral-400">Not in scope: only accounts that never connected Rivian are purged (never admins or disabled accounts).</p>
            )}
          </div>
        </Card>
      </div>

      <Card title={`Vehicles (${d ? d.vehicles.length : "…"})`}>
        {!d ? <Spinner /> : d.vehicles.length === 0 ? <p className="text-sm text-neutral-400">No vehicles on file.</p> : (
          <ul className="space-y-2">
            {d.vehicles.map((v) => (
              <li key={v.id} className="rounded-md border border-neutral-800 px-3 py-2">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="text-sm text-neutral-100">
                    {v.display_name || v.model || v.rivian_vehicle_id}
                    {v.model_year ? ` · ${v.model_year}` : ""}
                    {v.pack_kwh ? ` · ${v.pack_kwh.toFixed(0)} kWh` : ""}
                  </span>
                  <span className="text-xs text-neutral-400" title={v.last_sample_at ?? ""}>{v.last_sample_at ? `last frame ${agoFrom(v.last_sample_at)}` : "no telemetry"}</span>
                </div>
                <div className="mt-1 flex flex-wrap gap-x-4 gap-y-0.5 text-xs text-neutral-400">
                  {v.vin && <span>VIN {v.vin}</span>}
                  <span>{v.drive_count} drives</span>
                  <span>{v.charge_count} charges</span>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>

      {d?.signup_request && (
        <Card title="Signup origin">
          <p className="text-sm text-neutral-300">
            Requested {new Date(d.signup_request.requested_at).toLocaleString()} · <span className={d.signup_request.status === "approved" ? "text-emerald-300" : d.signup_request.status === "rejected" ? "text-rose-300" : "text-amber-300"}>{d.signup_request.status}</span>
            {d.signup_request.decided_at ? ` · decided ${new Date(d.signup_request.decided_at).toLocaleString()}` : ""}
          </p>
          {d.signup_request.message && <p className="mt-2 whitespace-pre-wrap rounded-md border border-neutral-800 p-2 text-xs text-neutral-300">{d.signup_request.message}</p>}
        </Card>
      )}

      <Card title="Admin history"><UserAuditHistory userID={row.id} bare /></Card>

      <ActionsCard row={row} users={users} currentUserID={currentUserID} needsReauth={conn.state === "reauth"} />
    </>
  );
}

function Facts({ children }: { children: React.ReactNode }) {
  return <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2">{children}</dl>;
}
function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-xs text-neutral-400">{label}</dt>
      <dd className="text-neutral-100">{children}</dd>
    </div>
  );
}

function TelemetrySpan({ id, d }: { id: string; d: AdminUserDetail | undefined }) {
  if (!d) return <>…</>;
  if (!d.oldest_sample_at || !d.newest_sample_at) return <>no samples</>;
  const label = `${new Date(d.oldest_sample_at).toLocaleDateString()} → ${new Date(d.newest_sample_at).toLocaleDateString()}`;
  const logs = grafanaUserExploreURL(id, d.oldest_sample_at, d.newest_sample_at, "logs");
  const traces = grafanaUserExploreURL(id, d.oldest_sample_at, d.newest_sample_at, "traces");
  return (
    <>
      {label}
      {logs && <> · <a href={logs} target="_blank" rel="noopener noreferrer" className="text-emerald-300 underline underline-offset-2">logs ↗</a></>}
      {traces && <> · <a href={traces} target="_blank" rel="noopener noreferrer" className="text-emerald-300 underline underline-offset-2">traces ↗</a></>}
    </>
  );
}

type Pending = null | "promote" | "demote" | "toggle" | "delete";

function ActionsCard({ row, users, currentUserID, needsReauth }: { row: AdminUserRow; users: AdminUserRow[]; currentUserID: string; needsReauth: boolean }) {
  const navigate = useNavigate();
  const acts = useUserActions();
  const rivianActions = useRivianActions();
  const impersonationDisabled = useImpersonationDisabled();
  const [pending, setPending] = useState<Pending>(null);
  const busy = acts.busyID === row.id;
  const adminCount = users.filter((u) => u.role === "admin" && !u.disabled).length;
  const isSelf = row.id === currentUserID;
  const lastAdmin = row.role === "admin" && adminCount <= 1;
  const name = row.display_name || row.username;
  const ask = (q: string, go: () => void) => (
    <div role="alertdialog" aria-label="Confirm" className="mt-3 rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
      <p className="font-medium text-rose-200">{q}</p>
      <div className="mt-2 flex gap-2">
        <button type="button" disabled={busy} onClick={() => { setPending(null); go(); }} className="rounded-md bg-rose-700 px-3 py-1.5 text-sm font-semibold text-white hover:bg-rose-600 disabled:opacity-50">Confirm</button>
        <button type="button" onClick={() => setPending(null)} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200">Back</button>
      </div>
    </div>
  );
  const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-40";
  return (
    <Card title="Actions">
      <div data-user-actions className="flex flex-wrap items-center gap-2">
        {row.role === "user" ? (
          <button type="button" disabled={busy} className={btn} onClick={() => setPending("promote")}>Promote to admin…</button>
        ) : (
          <button type="button" disabled={busy || lastAdmin} title={lastAdmin ? "Cannot demote the last admin" : ""} className={btn} onClick={() => setPending("demote")}>Demote to user…</button>
        )}
        <button type="button" disabled={busy || (!row.disabled && (isSelf || lastAdmin))} title={!row.disabled && isSelf ? "Cannot disable your own account" : !row.disabled && lastAdmin ? "Cannot disable the last admin" : ""} className={btn} onClick={() => setPending("toggle")}>
          {row.disabled ? "Enable account…" : "Disable account…"}
        </button>
        {rivianActions && <button type="button" disabled={busy} className={btn} onClick={() => void acts.sync(row)}>Sync Rivian</button>}
        {rivianActions && needsReauth && <button type="button" disabled={busy} className={btn} onClick={() => void acts.refreshSession(row)}>Refresh session</button>}
        {row.role !== "admin" && !impersonationDisabled && (
          <button type="button" className={btn} onClick={() => { startImpersonation({ id: row.id, email: row.email || row.username }); window.location.assign("/"); }}>View as {name}</button>
        )}
        <button type="button" disabled={busy || isSelf || lastAdmin} title={isSelf ? "Cannot delete your own account" : lastAdmin ? "Cannot delete the last admin" : ""} className="ml-auto rounded-md border border-rose-800 px-3 py-1.5 text-sm text-rose-300 hover:border-rose-600 disabled:opacity-40" onClick={() => setPending("delete")}>
          Delete user…
        </button>
      </div>
      {!rivianActions && <p data-rivian-actions-note className="mt-2 text-xs text-neutral-400">Rivian actions run on rivolt.dev only</p>}
      {pending === "promote" && ask(`Make ${name} an admin?`, () => void acts.setRole(row, "admin"))}
      {pending === "demote" && ask(`Remove admin from ${name}?`, () => void acts.setRole(row, "user"))}
      {pending === "toggle" && ask(row.disabled ? `Let ${name} sign in again?` : `Block ${name} from signing in?`, () => void acts.toggleDisabled(row))}
      {pending === "delete" && (
        <div className="mt-3">
          <TypedConfirmDialog
            title={`Delete ${name}?`}
            detail="Their drives, charges and settings are removed permanently."
            phrase={row.email || row.username}
            confirmLabel="Delete user"
            onCancel={() => setPending(null)}
            onConfirm={() => { setPending(null); void acts.remove(row).then((ok) => ok && navigate("/admin/users")); }}
          />
        </div>
      )}
      {acts.note && <p role="status" className="mt-3 text-xs text-neutral-300">{acts.note}</p>}
    </Card>
  );
}
