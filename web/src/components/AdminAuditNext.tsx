import { useMemo, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { backend, type AdminAuditRow } from "../lib/api";
import { agoFrom } from "../lib/adminFormat";
import { AUDIT_ACTIONS, actionLabel, detailPairs, outcome } from "../lib/adminAudit";
import { Card, ErrorBox, Spinner } from "./ui";

// Admin > Audit (new look): every admin write, newest first, read-only. Filter by action, by who
// did it, or by the user it was about; "Load older" follows the server's cursor.
const PAGE = 50;

function useNames() {
  const users = useQuery({ queryKey: ["admin", "users"], queryFn: () => backend.adminListUsers(), retry: 0, staleTime: 60_000 });
  const list = users.data?.users ?? [];
  const name = (id?: string | null) => {
    if (!id) return null;
    const u = list.find((x) => x.id === id);
    return u ? u.display_name || u.email || u.username : id.slice(0, 8);
  };
  return { list, name };
}

export function AuditNext() {
  const [action, setAction] = useState("");
  const [actor, setActor] = useState("");
  const [target, setTarget] = useState("");
  const { list, name } = useNames();
  const q = useInfiniteQuery({
    queryKey: ["admin", "audit", "page", action, actor, target],
    queryFn: ({ pageParam }) => backend.adminAudit({ action, actor, target_user: target, limit: PAGE, before: pageParam }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => last.next_before ?? undefined,
    retry: 0,
  });
  const rows = useMemo(() => q.data?.pages.flatMap((p) => p.items) ?? [], [q.data]);
  const filtered = action !== "" || actor !== "" || target !== "";

  return (
    <div data-admin-audit className="space-y-4">
      <Card title="Audit log">
        <div className="space-y-3">
          <div className="flex flex-wrap items-end gap-3">
            <Select label="Action" value={action} onChange={setAction} options={[{ id: "", label: "All actions" }, ...AUDIT_ACTIONS.map((a) => ({ id: a.id, label: a.label }))]} />
            <Select label="Done by" value={actor} onChange={setActor} options={[{ id: "", label: "Anyone" }, ...list.filter((u) => u.role === "admin").map((u) => ({ id: u.id, label: u.display_name || u.email || u.username }))]} />
            <Select label="About user" value={target} onChange={setTarget} options={[{ id: "", label: "Any user" }, ...list.map((u) => ({ id: u.id, label: u.display_name || u.email || u.username }))]} />
            {filtered && (
              <button type="button" onClick={() => { setAction(""); setActor(""); setTarget(""); }} className="rounded-md border border-neutral-700 px-2.5 py-1.5 text-xs text-neutral-200 hover:border-neutral-500">
                Clear filters
              </button>
            )}
          </div>

          {q.isLoading && <Spinner />}
          {q.isError && <ErrorBox title="Failed to load the audit log" detail={String((q.error as Error).message)} />}
          {q.data && rows.length === 0 && <p className="text-sm text-neutral-400">{filtered ? "No entries match these filters." : "No admin actions recorded yet."}</p>}
          {rows.length > 0 && <AuditTable rows={rows} name={name} />}
          {q.hasNextPage && (
            <button type="button" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50">
              {q.isFetchingNextPage ? "Loading…" : "Load older"}
            </button>
          )}
          {q.data && !q.hasNextPage && rows.length > 0 && <p className="text-xs text-neutral-500">That is everything: {rows.length} entr{rows.length === 1 ? "y" : "ies"}.</p>}
        </div>
      </Card>
    </div>
  );
}

function Select({ label, value, onChange, options }: { label: string; value: string; onChange: (v: string) => void; options: { id: string; label: string }[] }) {
  return (
    <label className="flex flex-col gap-1 text-xs font-medium text-neutral-400">
      {label}
      <select value={value} onChange={(e) => onChange(e.target.value)} className="input !w-auto min-w-[10rem]">
        {options.map((o) => (
          <option key={o.id} value={o.id}>{o.label}</option>
        ))}
      </select>
    </label>
  );
}

export function AuditTable({ rows, name, compact = false }: { rows: AdminAuditRow[]; name: (id?: string | null) => string | null; compact?: boolean }) {
  return (
    <div tabIndex={0} role="region" aria-label="Admin history entries" className="overflow-x-auto rounded-md border border-neutral-800">
      <table className={`w-full text-sm ${compact ? "min-w-[460px]" : "min-w-[760px]"}`}>
        <thead className="text-left text-xs text-neutral-400">
          <tr>
            <th scope="col" className="px-3 py-2">When</th>
            {!compact && <th scope="col" className="px-3 py-2">Done by</th>}
            <th scope="col" className="px-3 py-2">Action</th>
            {!compact && <th scope="col" className="px-3 py-2">About</th>}
            <th scope="col" className="px-3 py-2">Result</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => <AuditRow key={r.id} r={r} name={name} compact={compact} />)}
        </tbody>
      </table>
    </div>
  );
}

function AuditRow({ r, name, compact }: { r: AdminAuditRow; name: (id?: string | null) => string | null; compact: boolean }) {
  const o = outcome(r);
  const pairs = detailPairs(r.details);
  const about = name(r.target_user_id) ?? (r.target_id ? `${r.target_type}: ${r.target_id}` : r.target_type);
  return (
    <tr data-audit-row={r.id} className="border-t border-neutral-800 align-top">
      <td className="px-3 py-2 text-xs tabular-nums text-neutral-300" title={new Date(r.at).toLocaleString()}>{agoFrom(r.at)}</td>
      {!compact && (
        <td className="px-3 py-2">
          <div className="text-neutral-100">{name(r.actor_user_id)}</div>
          <div className="text-xs text-neutral-400">{r.access_email ? `${r.access_email} · ` : ""}{r.ip}</div>
        </td>
      )}
      <td className="px-3 py-2">
        <div className="text-neutral-100">{actionLabel(r.action)}</div>
        {pairs.length > 0 && (
          <details className="text-xs text-neutral-400">
            <summary className="cursor-pointer text-neutral-300">Details</summary>
            <ul className="mt-1 space-y-0.5 font-mono">
              {pairs.map((p) => <li key={p} className="break-all">{p}</li>)}
            </ul>
          </details>
        )}
      </td>
      {!compact && <td className="px-3 py-2 text-neutral-300">{about}</td>}
      <td className="px-3 py-2">
        <span className={o.tone === "ok" ? "text-emerald-300" : o.tone === "bad" ? "text-rose-300" : "text-amber-300"}>{o.label}</span>
      </td>
    </tr>
  );
}

// The per-user history on the user detail: the last few actions taken about this user.
export function UserAuditHistory({ userID, bare = false }: { userID: string; bare?: boolean }) {
  const { name } = useNames();
  const q = useQuery({ queryKey: ["admin", "audit", "user", userID], queryFn: () => backend.adminAudit({ target_user: userID, limit: 10 }), retry: 0 });
  if (q.isLoading) return <Spinner />;
  if (q.isError) return null;
  const rows = q.data?.items ?? [];
  return (
    <section aria-label="Admin history" data-user-audit className="space-y-2">
      {!bare && <h3 className="text-xs font-semibold uppercase tracking-wide text-neutral-400">Admin history</h3>}
      {rows.length === 0 ? <p className="text-sm text-neutral-400">Nothing recorded about this user.</p> : <AuditTable rows={rows} name={name} compact />}
    </section>
  );
}
