import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type SignupRequest } from "../lib/api";
import { agoFrom } from "../lib/adminFormat";
import { Card, ErrorBox, Spinner } from "./ui";
import { ChangedBy } from "./AdminFlagsNext";

// Signups (new look): the cap with its usage up front, then the queue with approve and reject one
// click away. Readable at phone width: the table scrolls inside its own box.
const FILTERS = ["pending", "approved", "rejected", ""] as const;
type Filter = (typeof FILTERS)[number];

export function SignupsNext() {
  return (
    <div data-admin-signups className="space-y-4">
      <CapCard />
      <Queue />
    </div>
  );
}

function CapCard() {
  const qc = useQueryClient();
  const cap = useQuery({ queryKey: ["admin", "signup-cap"], queryFn: backend.adminSignupCapGet });
  const [draft, setDraft] = useState("");
  const save = useMutation({
    mutationFn: (limit: number) => backend.adminSignupCapPut(limit),
    onSuccess: (data) => {
      qc.setQueryData(["admin", "signup-cap"], data);
      qc.invalidateQueries({ queryKey: ["admin", "audit"] });
      setDraft("");
    },
  });
  if (cap.isLoading) return <Spinner />;
  if (cap.isError) return <ErrorBox title="Failed to load the signup cap" detail={(cap.error as Error).message} />;
  const limit = cap.data!.signup_cap.limit;
  const used = cap.data!.used;
  const left = Math.max(0, limit - used);
  const pct = limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 100;
  const parsed = draft.trim() === "" ? null : Number(draft);
  const valid = parsed !== null && Number.isInteger(parsed) && parsed >= 0;
  const full = limit === 0 || left === 0;
  return (
    <Card title="Signup cap">
      <div className="space-y-3 text-sm">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <div data-cap-usage className="text-lg font-semibold tabular-nums text-neutral-100">
            {used} / {limit} <span className="text-sm font-normal text-neutral-400">accounts</span>
          </div>
          <div className={`text-sm ${full ? "font-medium text-amber-300" : "text-neutral-300"}`}>
            {limit === 0 ? "Closed: no new signups" : full ? "Full: new signups are blocked" : `${left} seat${left === 1 ? "" : "s"} left`}
          </div>
        </div>
        <div
          role="progressbar"
          aria-label="Accounts against the signup cap"
          aria-valuemin={0}
          aria-valuemax={limit}
          aria-valuenow={Math.min(used, limit)}
          className="h-2.5 rounded-full bg-neutral-800"
        >
          <div className={`h-full rounded-full ${full ? "bg-amber-500" : "bg-emerald-500"}`} style={{ width: `${pct}%` }} />
        </div>
        <ChangedBy action="flag.signup_cap" />
        <p className="text-xs text-neutral-400">
          New signups stop once accounts reach the cap; people already on the system can always sign back in. 0 closes signups. Takes effect within about 10 seconds.
        </p>
        <div className="flex items-center gap-2">
          <input
            type="number"
            min={0}
            inputMode="numeric"
            aria-label="New signup cap"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder={String(limit)}
            className="input !w-32"
          />
          <button
            type="button"
            disabled={!valid || save.isPending || parsed === limit}
            onClick={() => valid && save.mutate(parsed)}
            className="rounded-md border border-emerald-700 bg-emerald-900/40 px-3 py-1.5 text-sm text-emerald-100 hover:bg-emerald-900/60 disabled:opacity-50"
          >
            {save.isPending ? "Saving…" : "Save cap"}
          </button>
        </div>
        {save.isError && <p role="alert" className="text-xs text-rose-300">Save failed: {String((save.error as Error).message)}</p>}
      </div>
    </Card>
  );
}

function Queue() {
  const qc = useQueryClient();
  const [filter, setFilter] = useState<Filter>("pending");
  const [approved, setApproved] = useState<{ email: string; link: string; sent: boolean } | null>(null);
  const list = useQuery({ queryKey: ["admin", "signup-requests", filter], queryFn: () => backend.adminListSignupRequests(filter) });
  const pendingCount = useQuery({ queryKey: ["admin", "signup-requests", "pending"], queryFn: () => backend.adminListSignupRequests("pending") });
  const approve = useMutation({
    mutationFn: (id: string) => backend.adminApproveSignupRequest(id),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ["admin", "signup-requests"] });
      qc.invalidateQueries({ queryKey: ["admin", "signup-cap"] });
      if (data.link) setApproved({ email: data.request.email, link: data.link, sent: data.email_sent });
    },
  });
  const reject = useMutation({
    mutationFn: (id: string) => backend.adminRejectSignupRequest(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "signup-requests"] }),
  });
  const busy = approve.isPending || reject.isPending;
  const nPending = pendingCount.data?.requests.length;
  return (
    <Card title="Signup requests">
      <div className="space-y-3">
        <div role="group" aria-label="Filter requests" className="flex flex-wrap gap-1.5">
          {FILTERS.map((s) => (
            <button
              key={s || "all"}
              type="button"
              aria-pressed={filter === s}
              onClick={() => setFilter(s)}
              className={`rounded-full border px-2.5 py-1 text-xs ${filter === s ? "border-emerald-600 bg-emerald-950/50 text-emerald-200" : "border-neutral-700 text-neutral-300 hover:border-neutral-500"}`}
            >
              {s || "all"}
              {s === "pending" && nPending !== undefined && <span className="ml-1 tabular-nums text-neutral-400">{nPending}</span>}
            </button>
          ))}
        </div>

        {approved && (
          <div role="status" className="rounded-md border border-emerald-900 bg-emerald-950/40 p-3 text-xs">
            <div className="flex items-center justify-between gap-2">
              <span className="text-emerald-300">Approved {approved.email}{approved.sent ? ": email sent" : ": email failed, copy the link below"}</span>
              <button type="button" onClick={() => setApproved(null)} className="text-neutral-300 underline underline-offset-2">Dismiss</button>
            </div>
            <div className="mt-1 flex items-center justify-between gap-2">
              <code className="break-all font-mono text-emerald-200">{approved.link}</code>
              <button type="button" onClick={() => void navigator.clipboard.writeText(approved.link)} className="shrink-0 text-neutral-300 underline underline-offset-2">Copy</button>
            </div>
          </div>
        )}

        {list.isLoading && <Spinner />}
        {list.isError && <ErrorBox title="Failed to load signup requests" />}
        {list.data && (
          list.data.requests.length === 0 ? (
            <p className="text-sm text-neutral-400">No signup requests in this view.</p>
          ) : (
            <div className="overflow-x-auto rounded-md border border-neutral-800">
              <table className="w-full min-w-[560px] text-sm">
                <thead className="text-left text-xs text-neutral-400">
                  <tr>
                    <th scope="col" className="px-3 py-2">Email</th>
                    <th scope="col" className="px-3 py-2">Message</th>
                    <th scope="col" className="px-3 py-2">Requested</th>
                    <th scope="col" className="px-3 py-2">Status</th>
                    <th scope="col" className="px-3 py-2 text-right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {list.data.requests.map((r) => <Row key={r.id} r={r} busy={busy} onApprove={() => approve.mutate(r.id)} onReject={() => reject.mutate(r.id)} />)}
                </tbody>
              </table>
            </div>
          )
        )}
        {(approve.isError || reject.isError) && (
          <p role="alert" className="text-xs text-rose-300">{String(((approve.error ?? reject.error) as Error).message)}</p>
        )}
      </div>
    </Card>
  );
}

function Row({ r, busy, onApprove, onReject }: { r: SignupRequest; busy: boolean; onApprove: () => void; onReject: () => void }) {
  const tone = r.status === "approved" ? "text-emerald-300" : r.status === "rejected" ? "text-rose-300" : "text-amber-300";
  const token = !r.signup_token ? null : r.token_used_at ? "signed up" : r.token_expires_at && Date.parse(r.token_expires_at) < Date.now() ? "link expired" : "link sent";
  return (
    <tr data-signup-row={r.id} className="border-t border-neutral-800 align-top">
      <td className="break-all px-3 py-2 font-mono text-xs text-neutral-100">{r.email}</td>
      <td className="whitespace-pre-wrap px-3 py-2 text-neutral-300">{r.message || <span className="text-neutral-500">—</span>}</td>
      <td className="px-3 py-2 text-xs text-neutral-400" title={r.requested_at}>{agoFrom(r.requested_at)}</td>
      <td className="px-3 py-2">
        <span className={tone}>{r.status}</span>
        {token && <div className="text-xs text-neutral-400">{token}</div>}
      </td>
      <td className="px-3 py-2 text-right">
        {r.status === "pending" ? (
          <div className="inline-flex gap-1.5">
            <button type="button" disabled={busy} aria-label={`Approve ${r.email}`} onClick={onApprove} className="rounded-md bg-emerald-700 px-2.5 py-1 text-xs font-semibold text-white hover:bg-emerald-600 disabled:opacity-50">Approve</button>
            <button type="button" disabled={busy} aria-label={`Reject ${r.email}`} onClick={onReject} className="rounded-md border border-neutral-600 px-2.5 py-1 text-xs text-neutral-200 hover:border-neutral-400 disabled:opacity-50">Reject</button>
          </div>
        ) : (
          <span className="text-xs text-neutral-500">decided</span>
        )}
      </td>
    </tr>
  );
}
